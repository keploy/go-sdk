package kredis

import (
	"github.com/go-redis/redis/v8"
	"go.uber.org/zap"
)

type RedisClient struct {
	*redis.Client
	log *zap.Logger
}

func NewRedisClient(c *redis.Client) *RedisClient {
	// Initialize a logger
	logger, _ := zap.NewProduction()
	defer func() {
		_ = logger.Sync() // flushes buffer, if any
	}()

	return &RedisClient{Client: c}
}
