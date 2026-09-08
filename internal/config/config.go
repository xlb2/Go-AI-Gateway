package config

import (
	"os"
	"strings"
)

type Config struct {
	DB       DBConfig
	Redis    RedisConfig
	RabbitMQ RabbitMQConfig
	Server   ServerConfig
}

type DBConfig struct {
	DSN string
}

type RedisConfig struct {
	Addr string
}

type RabbitMQConfig struct {
	URL       string
	QueueName string
}

type ServerConfig struct {
	HTTPAddr  string
	GRPCAddr  string
	PprofAddr string
}

// loadEnvFile 从项目根目录的 .env 文件加载环境变量。
// 项目约定（.gitignore 已忽略 .env）：把 API Key 等机密填在 .env 里，不进 git。
// 规则：已存在的环境变量优先，.env 不会覆盖它；空行和 # 注释跳过；支持 "a=b"、"a='b'"、"a=\"b\""。
func loadEnvFile() {
	data, err := os.ReadFile(".env")
	if err != nil {
		return // 没有 .env 文件就退回环境变量/默认值
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		if k == "" || os.Getenv(k) != "" {
			continue // 环境变量已存在时优先，不覆盖
		}
		os.Setenv(k, v)
	}
}

// LoadConfig 加载配置，优先级：环境变量 > .env 文件 > 默认值
func LoadConfig() *Config {
	loadEnvFile() // 先把 .env 读进环境变量，后面的 getEnv 才能读到
	return &Config{
		DB: DBConfig{
			DSN: getEnv("DB_DSN", "root:123456@tcp(127.0.0.1:3306)/im_gateway_db?charset=utf8mb4&parseTime=True&loc=Local"),
		},
		Redis: RedisConfig{
			Addr: getEnv("REDIS_ADDR", "localhost:6379"),
		},
		RabbitMQ: RabbitMQConfig{
			URL:       getEnv("MQ_URL", "amqp://guest:guest@localhost:5672/"),
			QueueName: "im_msg_queue",
		},
		Server: ServerConfig{
			HTTPAddr:  ":8080",
			GRPCAddr:  ":9090",
			PprofAddr: "localhost:6060",
		},
	}
}

func getEnv(key, defaultValue string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultValue
}
