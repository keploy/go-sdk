package kredis

import (
	"context"
	"errors"

	"github.com/go-redis/redis/v8"
	internal "github.com/keploy/go-sdk/internal/keploy"
	"github.com/keploy/go-sdk/keploy"

	"go.keploy.io/server/pkg/models"
)

// For Get Method

type KStringCmd struct {
	Val string
	Err string
}

func (rc *RedisClient) Get(ctx context.Context, key string) *redis.StringCmd {
	if internal.GetModeFromContext(ctx) == internal.MODE_OFF {
		return rc.Client.Get(ctx, key)
	}
	kctx, err := internal.GetState(ctx)
	var (
		resp   = &redis.StringCmd{}
		output = &KStringCmd{}
	)
	if err != nil {
		return resp
	}
	mode := kctx.Mode
	meta := map[string]string{
		"name":      "redis",
		"type":      string(models.NoSqlDB),
		"operation": "Get",
		"key":       key,
	}
	switch mode {
	case internal.MODE_TEST:
		// don't call the actual get method of redis
	case internal.MODE_RECORD:
		resp = rc.Client.Get(ctx, key)
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
		res := redis.NewStringCmd(ctx, "get", key)
		if output.Err != "" {
			res.SetErr(errors.New(output.Err))
		}
		res.SetVal(output.Val)
		return res
	}
	return resp
}
