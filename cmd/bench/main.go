package main

import (
	"flag"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// 定义物理指标计数器
var (
	successCount atomic.Int32
	failCount    atomic.Int32
)

func main() {
	// 1. 设置压测参数（默认压测 10000 个并发连接）
	concurrency := flag.Int("c", 2000, "并发连接数")
	port := flag.String("p", "8080", "网关端口号")
	path := flag.String("path", "/api/v1/ws", "WebSocket 路由路径") // 已经为你校准了真实路由
	flag.Parse()

	log.Printf("火力全开：准备向 127.0.0.1:%s%s 发起 %d 个并发长连接轰炸...\n", *port, *path, *concurrency)

	var wg sync.WaitGroup

	// 2. 协程并发启动：瞬间制造物理洪峰
	startTime := time.Now()
	for i := 0; i < *concurrency; i++ {
		wg.Add(1)
		go func(clientID int) {
			defer wg.Done()

			targetURL := fmt.Sprintf("ws://127.0.0.1:%s%s?uid=%d", *port, *path, clientID+1)

			// 建立连接
			dialer := websocket.DefaultDialer
			conn, _, err := dialer.Dial(targetURL, nil)
			if err != nil {
				failCount.Add(1)
				// 压测时不要打印每个失败日志，会把终端卡死，只记数即可
				return
			}
			defer conn.Close()

			// 记录成功数
			successCount.Add(1)

			// 3. 保持连接存活：模拟真实的客户端心跳
			// 只有死循环卡住，这个长连接才会在物理内存中一直存在
			for {
				// 每 10 秒发一次 Ping 保持活跃，防止被网关的巡逻队踢掉
				err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"ping"}`))
				if err != nil {
					return
				}
				time.Sleep(10 * time.Second)
			}
		}(i)

		// 极其微小的休眠，防止客户端自己的操作系统端口耗尽或 CPU 瞬间宕机
		time.Sleep(1 * time.Millisecond)
	}

	log.Printf("所有进攻协程发射完毕，耗时: %v\n", time.Since(startTime))
	log.Printf("当前战况 -> 成功建连: %d | 失败拒绝: %d\n", successCount.Load(), failCount.Load())
	log.Println("正在维持连接物理状态... (按 Ctrl+C 终止压测)")

	// 阻塞主协程，让子协程把连接死死撑住
	wg.Wait()
}
