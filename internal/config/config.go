package config

import "os"

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

// LoadConfig 加载配置，环境变量优先级高于默认值
func LoadConfig() *Config {
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
