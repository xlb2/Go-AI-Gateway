package handler

import (
	"context"
	"go_im_gateway/internal/ai_service"
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

// 定义一个专用的类型作为 Context 的 Key，这是 Go 官方极其强调的安全规范！
// 面试官考点：为什么不能用普通的 string 作为 key？为了防止不同包之间的键名冲突！
type traceKey string

const TraceIDKey traceKey = "x-trace-id"

type wrappedServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (w *wrappedServerStream) Context() context.Context {
	return w.ctx
}

func (s *AgentGatewayImpl) StreamChat(stream rpc.AgentGateway_StreamChatServer) error {
	log.Println("gRPC 双向流链接已经建立!")
	var seq int32 = 0
	for {
		in, err := stream.Recv()
		if err == io.EOF {
			log.Println("客户端主动断开了连接")
			return nil
		}
		if err != nil {
			log.Printf("接收流出错: %v\n", err)
			return err
		}
		log.Printf("收到客户端消息：Session = %s ,Query = %s\n", in.SessionId, in.UserQuery)
		seq++
		resp := &rpc.ChatResponse{
			SeqNum:    seq,
			DeltaText: "网关回声：" + in.UserQuery,
		}
		err = stream.Send(resp)
		if err != nil {
			log.Printf("发送流出错: %v\n", err)
			return err
		}
	}
}

// RateLimitInterceptor 制造一个 gRPC 流式拦截器
func RateLimitInterceptor(rdb *redis.Client) grpc.StreamServerInterceptor {
	return func(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {

		traceID := uuid.New().String()
		ctx := context.WithValue(ss.Context(), TraceIDKey, traceID)

		wrappedServerStream := &wrappedServerStream{
			ServerStream: ss,
			ctx:          ctx,
		}

		log.Printf("[TRACE: %s] 🚀 收到 gRPC 请求: %s", traceID, info.FullMethod)

		// 1. 【身份识别】从 gRPC 的上下文中提取用户的凭证（相当于 HTTP 的 Header）
		// 面试官考点：gRPC 里传 Header 必须用 metadata！
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
			log.Printf(" 捕捉到非法伪造 Token 攻击：%v", err)
			return status.Errorf(codes.Unauthenticated, " 拒绝访问：Token 无效或已过期")
		}
		userID := claims.UserID

		pass := ai_service.CheckRateLimit(wrappedServerStream.Context(), rdb, userID)
		if !pass {
			log.Printf(" [防线触发] 用户 %d 请求超载，已被物理熔断！", userID)
			return status.Errorf(codes.ResourceExhausted, "触发防御机制：您的请求过于频繁，请稍后再试！")
		}
		return handler(srv, wrappedServerStream)
	}
}
