package main

import (
	"context"
	"fmt"
	"go_im_gateway/internal/ai_service"
	"go_im_gateway/internal/dao"
	"go_im_gateway/internal/handler"
	"go_im_gateway/internal/model"
	"go_im_gateway/internal/service"
	"log" // [新增] 用于暴露 pprof
	"net"
	"net/http"
	_ "net/http/pprof" // [新增] 性能监控雷达
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rabbitmq/amqp091-go"
	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func main() {

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGALRM)
	defer stop()

	var wg sync.WaitGroup
	// // 启动最高级别物理性能雷达 ===
	// go func() {
	// 	fmt.Println("🚀 pprof 性能雷达已开启：访问 http://localhost:6060/debug/pprof/")
	// 	log.Println(http.ListenAndServe("localhost:6060", nil))
	// }()

	// === 1. 动态挂载 MySQL 坐标 ===
	dsn := os.Getenv("DB_DSN")
	if dsn == "" {
		dsn = "root:123456@tcp(127.0.0.1:3306)/im_gateway_db?charset=utf8mb4&parseTime=True&loc=Local"
	}
	var db *gorm.DB
	var err error
	for i := range 10 {
		db, err = gorm.Open(mysql.Open(dsn), &gorm.Config{})
		if err == nil {
			fmt.Printf(" 数据库物理连接成功！(重试次数: %d)\n", i)
			break
		}
		fmt.Printf(" 无法连接数据库，正在准备第 %d 次重试: %v\n", i, err)
		time.Sleep(2 * time.Second)
	}

	if err != nil {
		log.Fatalf("无法连接到数据库：%v ", err)
	}
	err = db.AutoMigrate(&model.User{}, &model.Message{})
	if err != nil {
		panic("数据库迁移失败: " + err.Error())
	}
	fmt.Println("数据库物理表结构已同步！")

	// === 2. 动态挂载 Redis 坐标 ===
	redisAddr := os.Getenv("REDIS_ADDR")
	if redisAddr == "" {
		redisAddr = "localhost:6379"
	}

	rdb := redis.NewClient(&redis.Options{
		Addr:     redisAddr,
		Password: "",
		DB:       0,
	})

	// === 3. 强行挂载 RabbitMQ 消息队列 ==
	mqURL := os.Getenv("MQ_URL")
	if mqURL == "" {
		mqURL = "amqp://guest:guest@localhost:5672/"
	}

	fmt.Printf("正在接驳 MQ 管道，坐标: [%s]\n", mqURL)

	var connMQ *amqp091.Connection
	for i := range 10 {
		connMQ, err = amqp091.Dial(mqURL)
		if err == nil {
			fmt.Printf(" MQ 物理链连接成功！(重试次数: %d)\n", i)
			break
		}
		fmt.Printf(" MQ 物理链路熔断，正在准备第 %d 次重试: %v\n", i, err)
		time.Sleep(2 * time.Second)
	}
	if err != nil {
		log.Fatalf("MQ 物理链路熔断: %v", err)
	}
	defer connMQ.Close()

	chMQ, err := connMQ.Channel()
	if err != nil {
		log.Fatalf("MQ 传送带开启失败: %v", err)
	}
	defer chMQ.Close()

	_, err = chMQ.QueueDeclare("im_msg_queue", true, false, false, false, nil)
	if err != nil {
		log.Fatalf("MQ 队列注册失败: %v", err)
	}
	fmt.Println("🐇 RabbitMQ 异步削峰管道已接通！")

	// === 4. 依赖注入与路由组装 ===
	_, err = rdb.Ping(context.Background()).Result()
	if err != nil {
		panic("极其致命的异常：Redis 基站连接失败: " + err.Error())
	}
	fmt.Println("🔥 Redis 内存基站已成功物理挂载！")

	ai_service.Rdb = rdb
	uHandler := &handler.UserHandler{DB: db}
	messageDAO := dao.NewMessageDAO(db)
	messageService := service.NewMessageService(messageDAO, rdb, chMQ)

	r := gin.Default()
	handler.StartHeartbeatChecker()

	v1 := r.Group("api/v1")
	{
		userGroup := v1.Group("/user")
		{
			userGroup.POST("/register", uHandler.Register)
			userGroup.POST("/login", uHandler.Login)
		}

		messageGroup := v1.Group("/message")
		messageGroup.Use(handler.JWTAuthMiddleware())
		{
			messageGroup.POST("/send", func(c *gin.Context) {
				fromUserIDObj, exists := c.Get("user_id")
				if !exists {
					c.JSON(500, gin.H{"error": "系统异常：获取不到用户信息"})
					return
				}
				fromUserID := fromUserIDObj.(uint)
				var req struct {
					ToUserID uint   `json:"to_user_id"`
					Content  string `json:"content"`
				}
				if err := c.ShouldBindJSON(&req); err != nil {
					c.JSON(400, gin.H{"error": "弹药规格不符合 JSON 标准"})
					return
				}
				err := messageService.SendPrivateMessage(fromUserID, req.ToUserID, req.Content)
				if err != nil {
					c.JSON(500, gin.H{"error": "子弹卡壳：" + err.Error()})
					return
				}
				c.JSON(200, gin.H{"message": "子弹已成功打入 RabbitMQ！MQ万岁！"})
			})
		}

		// WebSocket 升级端点
		v1.GET("/ws", handler.ConnectWS(messageService, rdb))
	}

	grpcServer := grpc.NewServer()

	lis, _ := net.Listen("tcp", ":9090")
	wg.Add(1)
	go func() {
		defer wg.Done()
		log.Println("gRPC 引擎启动...")
		if err := grpcServer.Serve(lis); err != nil {
			log.Printf("gRPC 异常或关闭: %v\n", err)
		}
	}()

	srv := &http.Server{
		Addr:    ":8080",
		Handler: r,
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		log.Println(" Gin 引擎启动...")
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Gin 异常: %v\n", err)
		}
	}()
	<-ctx.Done()
	log.Println("\n接收到关闭信号，正在执行优雅停机 (Graceful Shutdown)...")
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
