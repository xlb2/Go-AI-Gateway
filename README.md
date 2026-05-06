#  Go-AI-Gateway (流式全双工 AI Agent 协议网关)

基于 Go 语言与 Eino 框架打造的工业级、高并发大模型接入网关。绕过传统 HTTP 短连接瓶颈，实现真正的全双工流式交互与物理级状态管控。

## 核心特性 (Features)

* **全双工流式基座 (WebSocket)：** 自定义长连接协议，彻底解决传统大模型 HTTP 轮询带来的极高延迟。支持毫秒级打字机推流体验。
* **物理级宕机防御 (Redis 状态机)：** 摒弃易丢失的内存状态，基于 Redis Pipeline 与 Lua 脚本构建全局挂起/拦截状态机。即使遭遇容器强杀 (Kill -9)，系统防线依然强一致。
* **工业级记忆引擎 (Sliding Window Memory)：** 针对大模型 Token 成本黑洞，实现基于 DTO 数据防线的 `RPUSH + LTRIM` 滑动窗口记忆环路。精准控制上下文爆炸，完美避开第三方结构体反序列化陷阱。
*  **异步流量削峰 (RabbitMQ)：**引入 MQ 彻底物理隔离网关极速接入层与高耗时 AI 算力层，保障请求洪峰下的系统高可用性。
* **死神巡逻队机制：** 手写 Goroutine 级心跳探活与连接强回收机制，彻底封杀长连接场景下的 OOM 内存泄漏隐患。

## 技术栈 (Tech Stack)

* **核心语言：** Go (Golang)
* **AI 编排引擎：** Eino (ByteDance)
* **状态与缓存：** Redis (go-redis/v9)
* **消息中间件：** RabbitMQ (amqp091-go)
* **持久化：** MySQL + GORM
* **鉴权：** JWT (JSON Web Token)

## 快速启动 (Quick Start)

### 1. 环境依赖
请确保本地已安装 `Docker` 与 `docker-compose`。

### 2. 部署基础组件 (MySQL / Redis / MQ)
```bash
docker-compose up -d
```
### 3. 配置环境变量
在运行前，请注入你的大模型 API 密钥：

```bash

export API_KEY="your_api_key"
export ENDPOINT="your_model_endpoint"
```

### 4. 启动网关基座
```Bash

go run cmd/api/main.go
```
### 架构说明 (Architecture)
流量入口： 客户端携带 JWT 护照经 HTTP 升级为 WebSocket 长连接。

状态拦截： 流量触达 AI 引擎前，优先经过 Redis 分布式状态机探查，拦截高危指令触发的防御动作。

上下文重装： 内存引擎异步抽提并组装滑动窗口历史对话，装填 Eino 引擎弹药。

推流与捕获： Eino 图引擎点火输出 Stream，网关将数据实时砸向终端的同时，利用旁路 strings.Builder 收集 AI 响应，异步写入持久化记忆。