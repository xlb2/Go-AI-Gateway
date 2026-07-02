package bootstrap

import (
	"fmt"
	"log"
	"time"

	"github.com/rabbitmq/amqp091-go"
)

// InitRabbitMQ 初始化RabbitMQ连接、通道，声明业务持久化队列
func InitRabbitMQ(url, queueName string) (*amqp091.Connection, *amqp091.Channel) {
	fmt.Printf("正在接驳 MQ 管道，坐标: [%s]\n", url)

	var conn *amqp091.Connection
	var err error

	for i := 0; i < 10; i++ {
		conn, err = amqp091.Dial(url)
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

	ch, err := conn.Channel()
	if err != nil {
		log.Fatalf("MQ 传送带开启失败: %v", err)
	}

	// 声明持久化队列
	_, err = ch.QueueDeclare(queueName, true, false, false, false, nil)
	if err != nil {
		log.Fatalf("MQ 队列注册失败: %v", err)
	}
	fmt.Println("🐇 RabbitMQ 异步削峰管道已接通！")

	return conn, ch
}
