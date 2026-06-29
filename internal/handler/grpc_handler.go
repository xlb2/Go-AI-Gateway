package handler

import (
	"context"
	"go_im_gateway/internal/ai_service"
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

	//  核心动作：从 Context 里抠出接入层拦截器注入的 TraceID
	traceID, _ := ctx.Value(middleware.TraceIDKey).(string)
	if traceID == "" {
		traceID = "unknown-trace"
	}

	log.Printf("[TRACE: %s]  gRPC 双向流链接已经建立!", traceID)
	var seq int32 = 0
	for {
		in, err := stream.Recv()
		if err == io.EOF {
			log.Printf("[TRACE: %s]  客户端主动断开了连接", traceID)
			return nil
		}
		if err != nil {
			log.Printf("[TRACE: %s]  接收流出错: %v\n", traceID, err)
			return err
		}

		log.Printf("[TRACE: %s]  收到客户端消息：Session = %s ,Query = %s\n", traceID, in.SessionId, in.UserQuery)
		seq++
		resp := &rpc.ChatResponse{
			SeqNum:    seq,
			DeltaText: "网关回声：" + in.UserQuery,
		}
		err = stream.Send(resp)
		if err != nil {
			log.Printf("[TRACE: %s]  发送流出错: %v\n", traceID, err)
			return err
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

		pass := ai_service.CheckRateLimit(wrappedServerStream.Context(), rdb, userID)
		if !pass {
			log.Printf("[TRACE: %s]  [防线触发] 用户 %d 请求超载，已被物理熔断！", traceID, userID)
			return status.Errorf(codes.ResourceExhausted, "触发防御机制：您的请求过于频繁，请稍后再试！")
		}
		return handler(srv, wrappedServerStream)
	}
}
