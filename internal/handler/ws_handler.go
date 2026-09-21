package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"go_im_gateway/internal/harness"
	"go_im_gateway/internal/harness/appserver"
	"go_im_gateway/internal/model"
	"go_im_gateway/internal/service"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/redis/go-redis/v9"
)

// ======IM核心架构：中央交换机======
// Client 封装了光缆的物理指针，以及它的生命体征
type Client struct {
	SendMutex     sync.Mutex
	Conn          *websocket.Conn
	LastHeartbeat time.Time
	cancel        context.CancelFunc
}

// ClientManager 是一本全局花名册，记录【UserID】-> 光缆指针
var ClientManager = make(map[uint]*Client)

// MessagePayload 定义了光缆里传输的数据结构
type MessagePayload struct {
	Type     string `Json:"type"`       // 情报类型：是 "ping" 还是 "chat"？
	ToUserID uint   `json:"to_user_id"` //发给谁
	Content  string `json:"content"`    //说什么
	RunID    string `json:"run_id,omitempty"`
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
	fmt.Printf("警告：系统检测到高危指令！用户当前情绪: [%s]\n", emotion)
	fmt.Println("正在启动本地物理防御协议...")
	fmt.Println("动作 1：已开启高频限流盾！")
	fmt.Println("动作 2：已向系统管理员发送预警弹窗！")
}

func (c *Client) SendMessage(msg []byte) error {
	return c.sendFrame(websocket.TextMessage, msg)
}

func (c *Client) sendFrame(kind int, msg []byte) error {
	c.SendMutex.Lock()
	defer c.SendMutex.Unlock()
	if err := c.Conn.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
		if c.cancel != nil {
			c.cancel()
		}
		c.Conn.Close()
		return err
	}
	err := c.Conn.WriteMessage(kind, msg)
	if err != nil {
		if c.cancel != nil {
			c.cancel()
		}
		c.Conn.Close()
	}
	return err
}

type ChatMessages interface {
	PullOfflineMessages(uint) ([]model.Message, error)
	SendPrivateMessage(uint, uint, string) error
}

// 带透视眼的安保队长2.0
func ConnectWS(msgService *service.MessageService, rdb *redis.Client) gin.HandlerFunc {
	return ConnectWSWithRunner(msgService, rdb, harness.Default)
}

func ConnectWSWithRunner(msgService ChatMessages, rdb *redis.Client, runner appserver.AgentRunner) gin.HandlerFunc {
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
		claims, err := ParseToken(tokenString)
		if err != nil {
			fmt.Println("物理拦截：护照已过期或被篡改！")
			c.JSON(http.StatusUnauthorized, gin.H{"error": "无效的护照"})
			return
		}

		//======核心的第三步：提取身份信息=======
		userID := claims.UserID //拿到身份证明
		//只有身份核实无误，才允许执行光缆的升级
		conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
		if err != nil {
			fmt.Println("光缆架设失败:", err)
			return
		}
		connectionCtx, cancel := context.WithCancel(context.WithValue(c.Request.Context(), "user_id", userID))
		defer cancel()
		client := &Client{Conn: conn, LastHeartbeat: time.Now(), cancel: cancel}
		stopClose := context.AfterFunc(connectionCtx, func() { conn.Close() })
		defer stopClose()
		var busy atomic.Bool
		//======核心战术动作1：上锁，登记召册====
		ClientMUtex.Lock()

		// 1.  顶号防御机制：检查该 UserID 是否已经有旧连接存在
		if oldClient, exists := ClientManager[userID]; exists {
			fmt.Printf("【系统警告】检测到 UserID: %d 发生多端登录！正在强制掐断旧连接...\n", userID)
			// 2.  核心修复：物理拔掉旧连接的网线！绝对不能漏掉这一步，否则直接 FD 泄漏！
			if oldClient.cancel != nil {
				oldClient.cancel()
			}
			oldClient.Conn.Close()
		}

		// 3. 安全登记新连接
		ClientManager[userID] = client

		// 4. 计算当前真实在线人数 (直接拿 Map 的长度最准，不需要自己搞个容易 Data Race 的变量)
		currentOnline := len(ClientManager)

		ClientMUtex.Unlock()

		fmt.Printf("【系统广播】UserID: %d 已登记入册！当前真实在线人数: %d\n", userID, currentOnline)

		//核心战术2：设置拔网线时的物理回收机制====
		defer func() {
			ClientMUtex.Lock() //准备拔网线，再次锁死
			if ClientManager[userID] == client {
				delete(ClientManager, userID)
			}
			ClientMUtex.Unlock()
			conn.Close()
			fmt.Printf("【系统广播】UserID: %d 已彻底断开，内存已回收\n", userID)
		}()

		offlineMessages, _ := msgService.PullOfflineMessages(userID)

		if len(offlineMessages) > 0 {
			fmt.Printf("【系统广播】正在为 UserID %d 补发 %d 条离线消息...\n", userID, len(offlineMessages))
			for _, msg := range offlineMessages {
				outbound := fmt.Sprintf("【离线补发 - 来自 UserID %d】: %s", msg.FromUserID, msg.Content)
				client.SendMessage([]byte(outbound))
			}

		}

		// 1. 确定私人频道的物理频段：每个用户一个专属频道，比如 "user:3:channel"
		channelName := fmt.Sprintf("user:%d:channel", userID)

		// 2. 向 Redis 塔台申请订阅
		pubsub := rdb.Subscribe(connectionCtx, channelName)
		defer pubsub.Close()

		// 3. 极其核心：劈开平行宇宙！派一个独立的侦察兵去死等 Redis 塔台
		go func() {
			// 订阅由连接入口负责关闭，断线时也能释放没有收到任何消息的订阅。

			//拿到无线电接收器
			ch := pubsub.Channel()

			// 开始死循环监听无线电
			for msg := range ch {
				fmt.Printf("【Redis 塔台】截获发给 UserID %d 的跨节点情报: %s\n", userID, msg.Payload)
				// 拿到情报后，顺着手里这根 WebSocket 光缆，直接砸向前端屏幕！
				err := client.SendMessage([]byte(msg.Payload))
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
			if ClientManager[userID] == client {
				client.LastHeartbeat = time.Now()
			}
			ClientMUtex.Unlock()
			fmt.Printf("收到来自 UserID %d 的情报: %s\n", userID, string(message))

			//解析情报，提取坐标====
			var payload MessagePayload
			import_json_err := json.Unmarshal(message, &payload)
			if import_json_err != nil {
				client.sendFrame(messageType, []byte("情报格式错误，必须是 JSON！"))
				continue
			}
			//=====心跳拦截与生命体征的刷新=====
			if payload.Type == "ping" {
				// 只需要给前端回一个响声，证明服务器还活着
				client.sendFrame(messageType, []byte(`{"type":"pong","content":"活着呢"}`))
				continue
			}
			if payload.Type == "agent/status" || payload.Type == "agent/cancel" {
				response := map[string]any{"type": payload.Type}
				control, ok := runner.(appserver.RunController)
				if !ok {
					response["error"] = "runner does not support run control"
				} else if payload.Type == "agent/status" {
					info, active := control.ActiveRun(userID)
					response["active"] = active
					if active {
						response["run"] = info
					}
				} else if strings.TrimSpace(payload.RunID) == "" {
					response["error"] = "run_id is required"
				} else {
					response["cancel_requested"] = control.CancelRun(userID, payload.RunID)
				}
				data, _ := json.Marshal(response)
				client.SendMessage(data)
				continue
			}

			if payload.ToUserID == 999 {

				if !busy.CompareAndSwap(false, true) {
					client.SendMessage([]byte("当前连接已有任务运行，请等待结束或取消该任务。"))
					continue
				}
				go func(payload MessagePayload, messageType int) {
					defer busy.Store(false)
					// 1. 声明 Context
					ctx, cancel := context.WithTimeout(connectionCtx, 60*time.Second)
					// 2. 这里的 defer 是安全的！因为它只在这个匿名函数结束时触发，不会堆积在外部的死循环里！
					defer cancel()
					ctx = context.WithValue(ctx, "user_id", userID)

					h := runner

					// 3. 人在回路审批：有待审批的高危任务时，只处理 auth:approve / auth:reject
					if handled, reply := h.HandleApprovalCommand(ctx, userID, strings.TrimSpace(payload.Content)); handled {
						client.sendFrame(messageType, []byte(reply))
						return
					}

					// 4. 跑一轮 agent 对话（pre钩子→记忆→Eino循环→落盘→post钩子），流式推给前端
					if _, err := h.RunAgentTurn(ctx, userID, payload.Content, func(chunk string) {
						client.sendFrame(messageType, []byte(chunk))
					}); err != nil {
						failMsg := fmt.Sprintf("Agent 执行失败: %v", err)
						fmt.Println(failMsg)
						client.sendFrame(messageType, []byte(failMsg))
					}
				}(payload, messageType)

				// 读循环继续接收控制消息，任务返回后才释放连接的忙标记。
				continue
			}
			err = msgService.SendPrivateMessage(userID, payload.ToUserID, payload.Content)
			if err != nil {
				client.sendFrame(messageType, []byte("系统警告：消息发送失败 "+err.Error()))
			} else {
				client.sendFrame(messageType, []byte("系统：炮弹已升空，已交由参谋部全网路由！"))
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
				// 物理法则判定：如果当前时间 减去 最后心跳时间，超过了 24 小时
				if now.Sub(client.LastHeartbeat) > 24*time.Hour {
					fmt.Printf("【死神巡逻队】警告：UserID %d 失去生命体征超过24小时，执行物理超度！\n", uid)
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
