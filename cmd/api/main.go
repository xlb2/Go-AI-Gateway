package main

import (
	"context"
	"fmt"
	"go_im_gateway/internal/ai_service"
	"go_im_gateway/internal/bootstrap"
	"go_im_gateway/internal/config"
	"go_im_gateway/internal/dao"
	"go_im_gateway/internal/handler"
	"go_im_gateway/internal/harness/agent"
	"go_im_gateway/internal/harness/approval"
	"go_im_gateway/internal/harness/hooks"
	"go_im_gateway/internal/harness/session"
	"go_im_gateway/internal/harness/spill"
	"go_im_gateway/internal/harness/subagent"
	"go_im_gateway/internal/service"
	"log"
	"net/http"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

func main() {
	// 1. 加载全局配置
	cfg := config.LoadConfig()

	// 2. 监听系统信号，支持优雅停机
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var wg sync.WaitGroup

	// 3. 启动 pprof 性能监控
	go func() {
		fmt.Printf(" pprof 性能雷达已开启：访问 http://%s/debug/pprof/\n", cfg.Server.PprofAddr)
		log.Println(http.ListenAndServe(cfg.Server.PprofAddr, nil))
	}()

	// 4. 初始化底层基础设施
	db := bootstrap.InitDB(cfg.DB.DSN)
	rdb := bootstrap.InitRedis(cfg.Redis.Addr)
	mqConn, mqCh := bootstrap.InitRabbitMQ(cfg.RabbitMQ.URL, cfg.RabbitMQ.QueueName)
	defer mqConn.Close()
	defer mqCh.Close()

	// 4.5 历史消息迁移：老数据按 (from,to) 自动补建会话
	migrationDAO := dao.NewMessageDAO(db)
	if err := migrationDAO.MigrateLegacyMessages(); err != nil {
		log.Printf("历史消息迁移失败（不影响启动）: %v\n", err)
	}

	// 5. 依赖注入：按 DAO -> Service -> Handler 顺序组装
	ai_service.Rdb = rdb         // 兼容 legacy 记忆/状态函数
	session.Init(rdb)            // harness 记忆器官注入 Redis
	approval.Init(rdb)           // harness 审批器官注入 Redis
	spill.Init(rdb)              // harness 溢出存储器官注入 Redis
	hooks.RegisterDefaultHooks() // 内置横切钩子（敏感词/超长拦截 + 回复统计）
	// 子智能体器官注入"造子 agent"的构造器（避免 subagent 包反向依赖 agent 包）
	subagent.SetRunner(func(ctx context.Context) (subagent.ChildAgent, error) {
		return agent.BuildEinoAgent(ctx)
	})
	messageDAO := dao.NewMessageDAO(db)
	messageService := service.NewMessageService(messageDAO, rdb, mqCh)
	messageService.StartConsumer() // 启动 MQ 消费者：异步把消息落盘到 MySQL
	userHandler := &handler.UserHandler{DB: db}

	// 6. 组装 HTTP 路由
	ginEngine := bootstrap.InitGinRouter(userHandler, messageService, rdb)

	// 7. 初始化 gRPC 服务
	grpcServer, grpcLis := bootstrap.InitGRPCServer(cfg.Server.GRPCAddr, rdb)

	// 8. 启动 gRPC 服务
	wg.Add(1)
	go func() {
		defer wg.Done()
		log.Println("gRPC 引擎启动...")
		if err := grpcServer.Serve(grpcLis); err != nil {
			log.Printf("gRPC 异常或关闭: %v\n", err)
		}
	}()

	// 9. 启动 HTTP 服务
	srv := &http.Server{
		Addr:    cfg.Server.HTTPAddr,
		Handler: ginEngine,
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		log.Println(" Gin 引擎启动...")
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Gin 异常: %v\n", err)
		}
	}()

	// 10. 阻塞等待关闭信号
	<-ctx.Done()
	log.Println("\n接收到关闭信号，正在执行优雅停机 (Graceful Shutdown)...")

	// 11. 执行优雅停机
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	log.Println("正在清空 HTTP 残留请求...")
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf(" HTTP 强制关闭: %v\n", err)
	}

	log.Println(" 正在清空 gRPC 流式残留...")
	grpcServer.GracefulStop()

	log.Println(" 正在断开底层存储连接...")
	wg.Wait()

	log.Println(" 优雅停机完毕，网关物理进程安全退出。")
}
