package service

import (
	"context"
	"encoding/json"
	"fmt"
	"go_im_gateway/internal/dao"
	"go_im_gateway/internal/model"

	"github.com/rabbitmq/amqp091-go"
	"github.com/redis/go-redis/v9"
)

// MessageService 是整个 IM 消息流转的核心大脑
type MessageService struct {
	Dao       *dao.MessageDAO // 左手握着 MySQL 的写入权限
	Rdb       *redis.Client   // 右手握着 Redis 塔台的广播电台
	MqChannel *amqp091.Channel
}

// NewMessageService 构造函数：执行大厂标准的“依赖注入”
func NewMessageService(dao *dao.MessageDAO, rdb *redis.Client, mqchannel *amqp091.Channel) *MessageService {
	return &MessageService{
		Dao:       dao,
		Rdb:       rdb,
		MqChannel: mqchannel,
	}
}

// SendPrivateMessage 战术动作 1：发送私信的完整物理连招
func (s *MessageService) SendPrivateMessage(fromUserID, toUserID uint, content string) error {
	// 第一步：穿防弹衣，下令 DAO 层物理落盘
	newMsg := &model.Message{
		FromUserID: fromUserID,
		ToUserID:   toUserID,
		Content:    content,
		IsRead:     false, // 分布式法则：发送时一律当做未读
	}
	msgBytes, _ := json.Marshal(newMsg)
	err := s.MqChannel.PublishWithContext(context.Background(),
		"",             // exchange（默认）
		"im_msg_queue", // 刚才声明的队列名字（routing key）
		false, false,
		amqp091.Publishing{
			ContentType:  "application/json",
			Body:         msgBytes,
			DeliveryMode: amqp091.Persistent, // 告诉 MQ：就算你重启，消息也不能丢！
		})
	if err != nil {
		return fmt.Errorf("消息扔进队列失败: %v", err)
	}

	// 第二步：呼叫塔台，全网广播
	targetChannel := fmt.Sprintf("user:%d:channel", toUserID)

	payload := map[string]interface{}{
		"type":         "chat",
		"from_user_id": fromUserID,
		"to_user_id":   toUserID,
		"content":      content,
	}
	outboundMeg, _ := json.Marshal(payload)

	if err := s.Rdb.Publish(context.Background(), targetChannel, outboundMeg).Err(); err != nil {
		return fmt.Errorf("Redis 广播失败: %v", err)
	}
	return nil
}

// PullOfflineMessages 战术动作 2：拉取离线消息
func (s *MessageService) PullOfflineMessages(userID uint) ([]model.Message, error) {
	return s.Dao.GetAndMarkOfflineMessage(userID)
}

// StartConsumer 开启后台清道夫协程，专门负责把 MQ 里的消息搬运到 MySQL
func (s *MessageService) StartConsumer() {
	// 1. 向 RabbitMQ 申请开启一条消费者通道
	msgs, err := s.MqChannel.Consume(
		"im_msg_queue", // 要监听的队列名字
		"",             // 消费者标识（留空自动生成）
		false,          // 【极其关键：关闭自动确认(Auto-Ack)】我们要等存入数据库后，手动确认！
		false,          // 是否排他
		false,          // 是否不能将同Connection中生产者发的消息传递给这个消费者
		false,          // 是否阻塞
		nil,            // 额外参数
	)
	if err != nil {
		fmt.Printf("🔴 消费者通道开启失败: %v\n", err)
		return
	}
	fmt.Println("🎧 异步落盘清道夫已就位，正在监听 RabbitMQ...")
	go func() {
		for d := range msgs {
			// d.Body 就是你刚才打进来的那颗子弹（JSON 格式的字节流）
			var msg model.Message
			if err := json.Unmarshal(d.Body, &msg); err != nil {
				fmt.Printf("解析子弹失败: %v\n", err)
				// 解析失败说明是脏数据，直接丢弃，不要阻塞队列
				d.Reject(false)
				continue
			}
			// 3. 极其冷酷地执行物理落盘
			if err := s.Dao.SaveMessage(&msg); err != nil {
				fmt.Printf("硬盘落盘失败，准备重试: %v\n", err)
				// 如果数据库出问题，把消息重新塞回队列（Nack），绝不能丢！
				d.Nack(false, true)
				continue
			}
			// 4. 落盘成功！向 RabbitMQ 发送物理回执，把这颗子弹从内存中销毁！
			// 这就是大厂保证消息 100% 绝对落盘的终极奥义！
			d.Ack(false)
			fmt.Printf("✅ 消息异步落盘成功！发送者: %d, 接收者: %d\n", msg.FromUserID, msg.ToUserID)
		}
	}()
}
