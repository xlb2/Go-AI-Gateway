package main

import (
	"context"
	"fmt"
	"go_im_gateway/internal/ai_service"
	"go_im_gateway/internal/dao"
	"go_im_gateway/internal/handler"
	"go_im_gateway/internal/model"
	"go_im_gateway/internal/service"
	"log"
	"os"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rabbitmq/amqp091-go"
	"github.com/redis/go-redis/v9"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func main() {
	// === 1. 动态挂载 MySQL 坐标 ===
	dsn := os.Getenv("DB_DSN")
	if dsn == "" { // 如果环境变量为空（比如在本地裸跑），就用兜底的本地坐标
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
		Addr:     redisAddr, // 使用动态注入的坐标
		Password: "",
		DB:       0,
	})
	// === 3. 强行挂载 RabbitMQ 消息队列 ===
	mqURL := "amqp://guest:guest@rabbitmq:5672/"

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

	// 声明一个名为 "im_msg_queue" 的队列（如果不存在会自动创建）
	// 参数: 名字, 是否持久化(durable), 是否自动删除, 是否排他, 是否阻塞, 额外参数
	_, err = chMQ.QueueDeclare("im_msg_queue", true, false, false, false, nil)
	if err != nil {
		log.Fatalf("MQ 队列注册失败: %v", err)
	}
	fmt.Println("🐇 RabbitMQ 异步削峰管道已接通！")
	//=== 4. 依赖注入大换血 ===
	// 现在你的 Service 不仅有 Dao，有 Rdb，还要有 Mq 管道！
	// 注意：你原来的 NewMessageService 需要修改，把 chMQ 传进去

	// 发射一次代码级的 Ping，验证物理连通性
	_, err = rdb.Ping(context.Background()).Result()
	if err != nil {
		panic("极其致命的异常：Redis 基站连接失败: " + err.Error())
	}

	fmt.Println("🔥 Redis 内存基站已成功物理挂载！")
	ai_service.Rdb = rdb

	uHandler := &handler.UserHandler{DB: db}
	// 1. 组装后勤仓库 (DAO)
	messageDAO := dao.NewMessageDAO(db)
	// 2. 将仓库和 Redis 塔台注入到作战参谋部 (Service)
	messageService := service.NewMessageService(messageDAO, rdb, chMQ)

	r := gin.Default()
	// === 召唤独立的死神巡逻队后台协程 ===
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
				//物理强转护照上的ID
				fromUserID := fromUserIDObj.(uint)
				var req struct {
					ToUserID uint   `json:"to_user_id"`
					Content  string `json:"content"`
				}
				if err := c.ShouldBindJSON(&req); err != nil {
					c.JSON(400, gin.H{"error": "弹药规格不符合 JSON 标准"})
					return
				}
				// 🔥 终极点火：调用上面已经造好的 messageService，直接把子弹轰进 MQ
				err := messageService.SendPrivateMessage(fromUserID, req.ToUserID, req.Content)
				if err != nil {
					c.JSON(500, gin.H{"error": "子弹卡壳：" + err.Error()})
					return
				}
				c.JSON(200, gin.H{"message": "子弹已成功打入 RabbitMQ！MQ万岁！"})
			})
		}
		v1.GET("/ws", handler.ConnectWS(messageService, rdb))
	}

	messageService.StartConsumer()
	fmt.Println("网关启动,正在监听8080端口....")
	r.Run(":8080")
}
