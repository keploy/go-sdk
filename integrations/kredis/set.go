package kredis

import (
	"context"
	"errors"
	"fmt"

	"time"

	"github.com/go-redis/redis/v8"
	"github.com/keploy/go-sdk/keploy"
	internal "github.com/keploy/go-sdk/pkg/keploy"

	"go.keploy.io/server/pkg/models"
)

// For Set method

type KStatusCmd struct {
	Val string
	Err string
}

func (rc *RedisClient) Set(ctx context.Context, key string, value interface{}, expiration time.Duration) *redis.StatusCmd {
	if internal.GetModeFromContext(ctx) == internal.MODE_OFF {
		return rc.Client.Set(ctx, key, value, expiration)
	}
	kctx, err := internal.GetState(ctx)
	var (
		resp   = &redis.StatusCmd{}
		output = &KStatusCmd{}
	)
	if err != nil {
		return resp
	}
	mode := kctx.Mode
	meta := map[string]string{
		"name":      "redis",
		"type":      string(models.NoSqlDB),
		"operation": "Set",
		"key":       key,
		"value":     fmt.Sprintf("%v", value),
		"expire":    fmt.Sprintf("%v", expiration),
	}
	switch mode {
	case internal.MODE_TEST:
		// don't call the actual set method of redis
	case internal.MODE_RECORD:
		resp = rc.Client.Set(ctx, key, value, expiration)
		x, er := resp.Result()
		output.Val = x
		if er != nil {
			output.Err = er.Error()
		}
	default:
		return resp

	}
	mock, _ := keploy.ProcessDep(ctx, rc.log, meta, output)
	if mock {
		resp = rc.Client.Set(ctx, key, value, expiration)
		if output.Err != "" {
			resp.SetErr(errors.New(output.Err))
		}
		resp.SetVal(output.Val)
	}
	return resp
}
