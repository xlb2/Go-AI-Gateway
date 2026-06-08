package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"go_im_gateway/internal/ai_service"
	"go_im_gateway/internal/service"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
	"github.com/redis/go-redis/v9"
)

// ======IM核心架构：中央交换机======
// Client 封装了光缆的物理指针，以及它的生命体征
type Client struct {
	SendMutex     sync.Mutex
	Conn          *websocket.Conn
	LastHeartbeat time.Time
}

// ClientManager 是一本全局花名册，记录【UserID】-> 光缆指针
var ClientManager = make(map[uint]*Client)

// MessagePayload 定义了光缆里传输的数据结构
type MessagePayload struct {
	Type     string `Json:"type"`       // 情报类型：是 "ping" 还是 "chat"？
	ToUserID uint   `json:"to_user_id"` //发给谁
	Content  string `json:"content"`    //说什么
}

// ClientMUtex是一把物理读写锁，死死防住高并发下的内存撕裂
var ClientMUtex sync.RWMutex

// 1.定义一个协议升级器（Upgrader）
// 它的物理作用就是：检查客户端发来的http的升级请求，如果没有问题就把协议暴力切换成WebSocket
var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

type AIResponse struct {
	Intent  string `json:"intent"`
	Emotion string `json:"emotion"`
	Reply   string `json:"reply"`
}

func ExecuteSystemCommand(emotion string) {
	fmt.Println("\n==================================================")
	fmt.Printf("⚠️ 警告：系统检测到高危指令！用户当前情绪: [%s]\n", emotion)
	fmt.Println("⚙️ 正在启动本地物理防御协议...")
	fmt.Println("✅ 动作 1：已开启高频限流盾！")
	fmt.Println("✅ 动作 2：已向系统管理员发送预警弹窗！")
}

func (c *Client) SendMessage(msg []byte) error {
	c.SendMutex.Lock()
	defer c.SendMutex.Unlock()
	return c.Conn.WriteMessage(websocket.TextMessage, msg)
}

// 带透视眼的安保队长2.0
func ConnectWS(msgService *service.MessageService, rdb *redis.Client) gin.HandlerFunc {
	return func(c *gin.Context) {

		// uidStr := c.Query("uid")
		// userIDInt, _ := strconv.Atoi(uidStr)
		// if userIDInt == 0 {
		// 	// 如果连 uid 都没带，随机发一个巨大的临时身份证，绝对防止 Map 键值碰撞！
		// 	userIDInt = rand.Intn(900000) + 100000
		// }
		// userID := uint(userIDInt) // 拿到唯一的身份证明！

		// // ==============================================================

		// // 只有身份核实无误，才允许执行光缆的升级
		// conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
		// if err != nil {
		// 	fmt.Println("光缆架设失败:", err)
		// 	return
		// }

		//===核心第一步，从url的尾巴上抠出通行证====
		tokenString := c.Query("token")
		if tokenString == "" {
			fmt.Println("物理拦截:无证人员试图连接光缆！")
			//HTTP阶段的拦截，直接返回401，不给升级协议的机会
			c.JSON(http.StatusUnauthorized, gin.H{"error": "请求未携带护照"})
			return
		}
		//=====核心的第二步：复用JWT的核查逻辑，现场验算防伪钢印=====
		token, err := jwt.ParseWithClaims(tokenString, &CustomClaims{}, func(token *jwt.Token) (interface{}, error) {
			return jwtSecret, nil
		})

		if err != nil || !token.Valid {
			fmt.Println("物理拦截：护照已过期或被篡改！")
			c.JSON(http.StatusUnauthorized, gin.H{"error": "无效的护照"})
			return
		}

		//======核心的第三步：提取身份信息=======
		claims, ok := token.Claims.(*CustomClaims)
		if !ok {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "护照载荷损坏"})
			return
		}
		userID := claims.UserID //拿到身份证明
		//只有身份核实无误，才允许执行光缆的升级
		conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
		if err != nil {
			fmt.Println("光缆架设失败:", err)
			return
		}
		//======核心战术动作1：上锁，登记召册====
		ClientMUtex.Lock()
		ClientManager[userID] = &Client{
			Conn:          conn,
			LastHeartbeat: time.Now(),
		}
		ClientMUtex.Unlock()
		fmt.Printf("【系统广播】UserID: %d 已登记入册！当前在线人数: %d\n", userID, len(ClientManager))

		//核心战术2：设置拔网线时的物理回收机制====
		defer func() {
			ClientMUtex.Lock()            //准备拔网线，再次锁死
			delete(ClientManager, userID) //从花名册种删除
			ClientMUtex.Unlock()
			conn.Close()
			fmt.Printf("【系统广播】UserID: %d 已彻底断开，内存已回收\n", userID)
		}()

		offlineMessages, _ := msgService.PullOfflineMessages(userID)

		if len(offlineMessages) > 0 {
			fmt.Printf("【系统广播】正在为 UserID %d 补发 %d 条离线消息...\n", userID, len(offlineMessages))
			for _, msg := range offlineMessages {
				outbound := fmt.Sprintf("【离线补发 - 来自 UserID %d】: %s", msg.FromUserID, msg.Content)
				conn.WriteMessage(websocket.TextMessage, []byte(outbound))
			}

		}

		// 1. 确定私人频道的物理频段：每个用户一个专属频道，比如 "user:3:channel"
		channelName := fmt.Sprintf("user:%d:channel", userID)

		// 2. 向 Redis 塔台申请订阅
		pubsub := rdb.Subscribe(context.Background(), channelName)

		// 3. 极其核心：劈开平行宇宙！派一个独立的侦察兵去死等 Redis 塔台
		go func() {
			// 防御装甲：当这个侦察兵阵亡（或者光缆断开）时，必须向 Redis 塔台退订频道，防止内存泄漏！
			defer pubsub.Close()

			//拿到无线电接收器
			ch := pubsub.Channel()

			// 开始死循环监听无线电
			for msg := range ch {
				fmt.Printf("【Redis 塔台】截获发给 UserID %d 的跨节点情报: %s\n", userID, msg.Payload)
				// 拿到情报后，顺着手里这根 WebSocket 光缆，直接砸向前端屏幕！
				err := conn.WriteMessage(websocket.TextMessage, []byte(msg.Payload))
				if err != nil {
					break
				}
			}

		}()

		for {

			messageType, message, err := conn.ReadMessage()
			if err != nil {
				break //异常断开，触发defer回收
			}
			ClientMUtex.Lock()
			if client, exists := ClientManager[userID]; exists {
				client.LastHeartbeat = time.Now()
			}
			ClientMUtex.Unlock()
			fmt.Printf("收到来自 UserID %d 的情报: %s\n", userID, string(message))

			//解析情报，提取坐标====
			var payload MessagePayload
			import_json_err := json.Unmarshal(message, &payload)
			if import_json_err != nil {
				conn.WriteMessage(messageType, []byte("情报格式错误，必须是 JSON！"))
				continue
			}
			//=====心跳拦截与生命体征的刷新=====
			if payload.Type == "ping" {
				// 只需要给前端回一个响声，证明服务器还活着
				conn.WriteMessage(messageType, []byte(`{"type":"pong","content":"活着呢"}`))
				continue
			}

			if payload.ToUserID == 999 {

				func() {
					// 1. 声明 Context
					ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
					// 2. 这里的 defer 是安全的！因为它只在这个匿名函数结束时触发，不会堆积在外部的死循环里！
					defer cancel()
					ctx = context.WithValue(ctx, "user_id", userID)

					pending, err := ai_service.GetpendingAction(ctx, userID)
					if err == nil && pending != nil {
						text := strings.TrimSpace(payload.Content)
						if text == "auth:approve" {
							ai_service.ClearPendingAction(ctx, userID)
							fmt.Printf("✅ [物理执行] 防御系统已启动！触发因素: %s\n", pending.Param)
							conn.WriteMessage(messageType, []byte("✅ 审批通过，防御系统已物理激活。"))
						} else if text == "auth:reject" {
							ai_service.ClearPendingAction(ctx, userID)
							conn.WriteMessage(messageType, []byte("❌ 审批已拒绝，动作取消。"))
						} else {
							conn.WriteMessage(messageType, []byte("⚠️ 系统当前有待审批的高危任务，请先输入 auth:approve 或 auth:reject。"))
						}

						return
					}

					sysMsg := schema.SystemMessage(`你是一个极其冷酷的网关保安。
					如果发现用户在愤怒抱怨、或者发出攻击性指令，不要安抚！必须立刻调用 execute_system_defense 工具！
					如果只是普通聊天，正常回复即可。`)

					usrMsg := schema.UserMessage(payload.Content)
					history, _ := ai_service.GetHistory(ctx, userID)

					var fullMessages []*schema.Message
					fullMessages = append(fullMessages, sysMsg)
					fullMessages = append(fullMessages, history...)
					fullMessages = append(fullMessages, usrMsg)

					// 异步存新消息（务必使用 Background，防止跟着当前对话一起被 cancel 取消掉）
					go ai_service.SaveMessage(context.Background(), userID, usrMsg)

					agentRunner, err := ai_service.BulidEinoAgent(ctx, "...", "...")
					if err != nil {
						failMsg := fmt.Sprintf("❌ Eino 引擎点火失败! 物理死因: %v", err)
						fmt.Println(failMsg)
						conn.WriteMessage(messageType, []byte(failMsg))
						return // 异常退出
					}

					responseStream, err := agentRunner.Stream(ctx, fullMessages)
					if err != nil {
						failMsg := fmt.Sprintf("❌ Eino 推流熔断! 死因: %v", err)
						fmt.Println(failMsg)
						conn.WriteMessage(messageType, []byte(failMsg))
						return // 异常退出
					}

					var aiFullResponse strings.Builder

					for {
						chunkArray, err := responseStream.Recv()
						if err != nil {
							break // 结束读流，跳出 for 循环，继续往下走
						}
						switch chunk := chunkArray.(type) {
						case *schema.Message:
							if chunk.Content != "" {
								aiFullResponse.WriteString(chunk.Content)
								conn.WriteMessage(messageType, []byte(chunk.Content))
							}
						case []*schema.Message:
							for _, c := range chunk {
								if c.Content != "" {
									aiFullResponse.WriteString(c.Content)
									conn.WriteMessage(messageType, []byte(c.Content))
								}
							}
						}
					}

					if aiFullResponse.Len() > 0 {
						go ai_service.SaveMessage(context.Background(), userID, schema.AssistantMessage(aiFullResponse.String(), nil))
					}
				}()

				// 匿名函数执行完毕，所有的临时变量、Context 会被干干净净地回收
				// 然后在外层的长连接里，我们继续等待下一句话
				continue
			}
			err = msgService.SendPrivateMessage(userID, payload.ToUserID, payload.Content)
			if err != nil {
				conn.WriteMessage(messageType, []byte(fmt.Sprintf("系统警告：消息发送失败 %v", err)))
			} else {
				conn.WriteMessage(messageType, []byte("系统：炮弹已升空，已交由参谋部全网路由！"))
			}

		}
	}
}

// StartHeartbeatChecker 召唤死神巡逻队，全局只启动一次
func StartHeartbeatChecker() {
	// go 关键字：直接劈出一条独立的时间线（Goroutine协程），让它在后台永远跑下去
	go func() {
		for {
			time.Sleep(10 * time.Second) // 巡逻频率：每 10 秒醒来扫视一圈
			now := time.Now()
			ClientMUtex.Lock() // 巡逻时必须锁门！不准任何人这时候进出花名册！
			for uid, client := range ClientManager {
				// 物理法则判定：如果当前时间 减去 最后心跳时间，超过了 90 秒
				if now.Sub(client.LastHeartbeat) > 90*time.Second {
					fmt.Printf("【死神巡逻队】警告：UserID %d 失去生命体征超过90秒，执行物理超度！\n", uid)
					// 物理四步连招的绝杀
					client.Conn.Close()        // 1. 强行剪断 TCP 光缆，击穿那个用户的 ReadMessage 死循环
					delete(ClientManager, uid) // 2. 从花名册中残忍抹除户籍

				}
			}
			ClientMUtex.Unlock() // 巡逻完毕，开门放行
		}
	}()
}

//全盲的接线员1.0
// // 2.WebSocket 专属接线员
// func ConnectWS(c *gin.Context) {
// 	//第一步：执行物理的升级！将c.Writer 和 c.Request 交给升级器
// 	//如果成功，返回conn就是那珍贵的双向电缆
// 	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)

// 	if err != nil {
// 		fmt.Println("光缆架设失败：", err)
// 		return
// 	}
// 	//极其关键的物理回收机制：当这个函数结束时，必修掐断光缆，否则会导致服务器内存泄漏爆炸
// 	defer conn.Close()
// 	fmt.Println("物理光缆已经接通！客户端已经链接.")

// 	//第二步：进入死循环（Infinite Loop）
// 	//既然是长连接，我们必修用一个死循环把这个函数死死卡住，不让它结束，时刻监听光缆的动静
// 	for {
// 		//尝试从光缆里面读取客户端发来的信息
// 		messageType, message, err := conn.ReadMessage()
// 		if err != nil {
// 			//如果客户端强行拔掉网线或者中断连接，ReadMessage会报错
// 			fmt.Println("客户端断开连接或者读取异常：", err)
// 			break //击碎死循环，执行defer conn.Close()销毁连接
// 		}
// 		//打印收到的信息
// 		fmt.Printf("收到的前线的信息：%s \n", string(message))

// 		//第三步：双全工展示！服务器主动顺着光缆把消息砸回去（Echo）
// 		reply := []byte("服务器已经收到你的消息：" + string(message))
// 		if err := conn.WriteMessage(messageType, reply); err != nil {
// 			fmt.Println("服务器回传消息失败:", err)
// 			break
// 		}
// 	}

// }

// func ConnectWS(c *gin.Context) {
// 	tokenString := c.Query("token")

// 	// === 强行加装：全方位显微镜阵列 ===
// 	fmt.Printf("\n======【安检大门监控日志】======\n")
// 	fmt.Printf("1. 前端扔过来的原味护照: %s\n", tokenString)

// 	token, err := jwt.ParseWithClaims(tokenString, &CustomClaims{}, func(token *jwt.Token) (interface{}, error) {
// 		return jwtSecret, nil
// 	})

// 	fmt.Printf("2. 密码学引擎返回的 Error: %v\n", err)
// 	if token != nil {
// 		fmt.Printf("3. 护照状态 (token.Valid): %v\n", token.Valid)
// 	}
// 	fmt.Printf("================================\n\n")

// 	if tokenString == "" {
// 		c.JSON(http.StatusUnauthorized, gin.H{"error": "请求未携带护照"})
// 		return
// 	}

// 	if err != nil || !token.Valid {
// 		fmt.Println("物理拦截：护照已过期或被篡改！")
// 		c.JSON(http.StatusUnauthorized, gin.H{"error": "无效的护照"})
// 		return
// 	}

// 	claims, ok := token.Claims.(*CustomClaims)
// 	if !ok {
// 		c.JSON(http.StatusUnauthorized, gin.H{"error": "护照载荷损坏"})
// 		return
// 	}
// 	userID := claims.UserID

// 	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
// 	if err != nil {
// 		fmt.Println("光缆架设失败:", err)
// 		return
// 	}
// 	defer conn.Close()

// 	fmt.Printf("【系统广播】物理光缆已接通！接入方真实身份 UserID: %d\n", userID)

// 	for {
// 		messageType, message, err := conn.ReadMessage()
// 		if err != nil {
// 			fmt.Printf("【系统广播】UserID: %d 已断开光缆连接\n", userID)
// 			break
// 		}
// 		fmt.Printf("收到来自 UserID %d 的情报: %s\n", userID, string(message))

// 		reply := []byte(fmt.Sprintf("服务器已收到，当前你的 UserID 是 %d", userID))
// 		if err := conn.WriteMessage(messageType, reply); err != nil {
// 			break
// 		}
// 	}
// }
