package bootstrap

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"
)

// InitRedis 初始化Redis连接并校验连通性
func InitRedis(addr string) *redis.Client {
	rdb := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: "",
		DB:       0,
	})

	_, err := rdb.Ping(context.Background()).Result()
	if err != nil {
		panic("极其致命的异常：Redis 基站连接失败: " + err.Error())
	}
	fmt.Println("Redis 内存基站已成功物理挂载！")

	return rdb
}
