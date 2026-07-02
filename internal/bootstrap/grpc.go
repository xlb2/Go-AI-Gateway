package bootstrap

import (
	"go_im_gateway/internal/handler"
	"net"

	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
)

// InitGRPCServer 初始化gRPC服务，返回服务实例和端口监听器
func InitGRPCServer(addr string, rdb *redis.Client) (*grpc.Server, net.Listener) {
	grpcServer := grpc.NewServer(
		grpc.StreamInterceptor(handler.RateLimitInterceptor(rdb)),
	)

	lis, err := net.Listen("tcp", addr)
	if err != nil {
		panic("gRPC 端口监听失败: " + err.Error())
	}

	return grpcServer, lis
}
