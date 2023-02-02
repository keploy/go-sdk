package kredis

import (
	"context"
	"errors"
	"fmt"

	"github.com/go-redis/redis/v8"
	"github.com/keploy/go-sdk/keploy"
	internal "github.com/keploy/go-sdk/pkg/keploy"

	"go.keploy.io/server/pkg/models"
)

// For Del Method

type KIntCmd struct {
	Val int64
	Err string
}

func (rc *RedisClient) Del(ctx context.Context, keys ...string) *redis.IntCmd {
	if internal.GetModeFromContext(ctx) == internal.MODE_OFF {
		return rc.Client.Del(ctx, keys...)
	}
	kctx, err := internal.GetState(ctx)
	var (
		resp   = &redis.IntCmd{}
		output = &KIntCmd{}
	)
	if err != nil {
		return resp
	}
	mode := kctx.Mode
	meta := map[string]string{
		"name":      "redis",
		"type":      string(models.NoSqlDB),
		"operation": "Del",
		"keys":      fmt.Sprintf("%v", keys),
	}
	switch mode {
	case internal.MODE_TEST:
		// don't call the actual del method of redis
	case internal.MODE_RECORD:
		resp = rc.Client.Del(ctx, keys...)
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
		resp = rc.Client.Del(ctx, keys...)
		if output.Err != "" {
			resp.SetErr(errors.New(output.Err))
		}
		resp.SetVal(output.Val)
	}
	return resp
}
