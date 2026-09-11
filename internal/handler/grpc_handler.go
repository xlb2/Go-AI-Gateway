package handler

import (
	"context"
	"go_im_gateway/internal/harness/guard"
	"go_im_gateway/internal/middleware"
	"go_im_gateway/rpc"
	"io"
	"log"
	"strings"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type AgentGatewayImpl struct {
	rpc.UnimplementedAgentGatewayServer
}

type wrappedServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (w *wrappedServerStream) Context() context.Context {
	return w.ctx
}

func (s *AgentGatewayImpl) StreamChat(stream rpc.AgentGateway_StreamChatServer) error {
	ctx := stream.Context()
	traceID, _ := ctx.Value(middleware.TraceIDKey).(string)

	log.Printf("[TRACE: %s]  gRPC 连接建立", traceID)

	//  核心公约1：落实简历上的 "严密的 defer 资源回收"
	defer func() {
		log.Printf("[TRACE: %s]  会话生命周期终结，清理机房内存", traceID)
	}()

	for {
		in, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}

		log.Printf("[TRACE: %s]  收到任务: %s", traceID, in.UserQuery)

		//  核心公约2：开辟 AI 算力缓冲通道
		// 注意：这里的数字 100 是生死攸关的配置！
		aiChunkChan := make(chan string, 100)
		errChan := make(chan error, 1)

		// 启动异步独立协程，去调下游的大模型（Eino引擎）
		go func(query string) {
			defer close(aiChunkChan) // 铁律：生产者协程执行完毕后，必须由生产者 close 通道！

			// 模拟调用 Eino 大模型流式 API（把带有 TraceID 和熔断树的 ctx 透传进去！）
			// CallEinoStream(ctx, query, aiChunkChan, errChan)

			// --- 这里暂时用假数据模拟大模型慢速吐字 ---
			chunks := []string{"思考中...", "基于Go的", "网关架构", "核心在于", "多路复用。"}
			for _, c := range chunks {
				aiChunkChan <- c
			}
		}(in.UserQuery)

		//  核心公约3：落实简历上的 "基于 Context 树的生命周期强制阻断机制"
		streamAlive := true
		var seq int32 = 0

		for streamAlive {
			select {
			case <-ctx.Done():
				//  触发物理现场：用户把网页关了 / 手机退网了！
				log.Printf("[TRACE: %s]  捕捉到客户端静默断连(Reason: %v)，立刻级联阻断下游AI协程！", traceID, ctx.Err())
				return ctx.Err() // 强行跳出函数，触发外层 defer

			case aiErr := <-errChan:
				log.Printf("[TRACE: %s]  下游 AI 算力节点暴雷: %v", traceID, aiErr)
				return aiErr

			case chunk, ok := <-aiChunkChan:
				if !ok {
					// 通道已被生产者 close，说明大模型本次回答完毕
					streamAlive = false
					break
				}
				seq++
				resp := &rpc.ChatResponse{
					SeqNum:    seq,
					DeltaText: chunk,
				}
				if sendErr := stream.Send(resp); sendErr != nil {
					log.Printf("[TRACE: %s]  网络下行推送失败: %v", traceID, sendErr)
					return sendErr
				}
			}
		}
	}
}

// RateLimitInterceptor 制造一个 gRPC 流式拦截器
func RateLimitInterceptor(rdb *redis.Client) grpc.StreamServerInterceptor {
	return func(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {

		traceID := uuid.New().String()
		ctx := context.WithValue(ss.Context(), middleware.TraceIDKey, traceID)

		wrappedServerStream := &wrappedServerStream{
			ServerStream: ss,
			ctx:          ctx,
		}

		log.Printf("[TRACE: %s]  收到 gRPC 请求: %s", traceID, info.FullMethod)

		md, ok := metadata.FromIncomingContext(wrappedServerStream.Context())
		if !ok {
			return status.Errorf(codes.Unauthenticated, "拒绝访问：未携带身份元数据")
		}

		authHanders := md.Get("authorization")
		if len(authHanders) == 0 {
			return status.Error(codes.Unauthenticated, "拒绝访问：缺失 Token")
		}

		tokenString := strings.TrimPrefix(authHanders[0], "Bearer ")

		claims, err := ParseToken(tokenString)
		if err != nil {
			log.Printf("[TRACE: %s]  捕捉到非法伪造 Token 攻击：%v", traceID, err)
			return status.Errorf(codes.Unauthenticated, "拒绝访问：Token 无效或已过期")
		}
		userID := claims.UserID

		pass := guard.AllowRequest(wrappedServerStream.Context(), rdb, userID)
		if !pass {
			log.Printf("[TRACE: %s]  [防线触发] 用户 %d 请求超载，已被物理熔断！", traceID, userID)
			return status.Errorf(codes.ResourceExhausted, "触发防御机制：您的请求过于频繁，请稍后再试！")
		}
		return handler(srv, wrappedServerStream)
	}
}
