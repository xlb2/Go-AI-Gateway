# 第一阶段：重装装甲编译厂 (Builder)
FROM golang:1.25-alpine AS builder
WORKDIR /app
# 极其关键：打通国内镜像源，否则下载依赖会超时报错
ENV GOPROXY=https://goproxy.cn,direct

# 先缓存依赖层（这是一种极其高级的镜像加速策略）
COPY go.mod go.sum ./
RUN go mod download

# 拷入全部代码
COPY . .

# 核心物理动作：指定你的主入口 cmd/api/main.go 进行无 CGO 纯净编译
RUN CGO_ENABLED=0 GOOS=linux go build -o im_gateway ./cmd/api/main.go

# 第二阶段：极简运行舱 (Runtime)
FROM alpine:latest


# 🚨 极其致命的补丁：给瞎子装上眼睛（全球 HTTPS 根证书和时区）！没有它，打不通大模型！
RUN apk --no-cache add ca-certificates tzdata


WORKDIR /app

# 把编译厂里造好的那颗“引擎”拿过来
COPY --from=builder /app/im_gateway .

# 暴露你的网关监听端口（假设你代码里写的是 8080）
EXPOSE 8080

# 点火启动
CMD ["./im_gateway"]