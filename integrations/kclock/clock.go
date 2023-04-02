package kclock

import (
	"context"
	"time"

	internal "github.com/keploy/go-sdk/pkg/keploy"
)

// Now returns an instance of time.Time. In record or off mode, the current time
// is returned.
// During test mode, time adjusted to testcase capture time is returned.
func Now(ctx context.Context) time.Time {
	// off mode
	if internal.GetModeFromContext(ctx) == internal.MODE_OFF {
		return time.Now()
	}

	var res time.Time
	kctx, er := internal.GetState(ctx)
	if er != nil {
		return time.Now()
	}
	mode := kctx.Mode

	switch mode {
	case internal.MODE_TEST:
		// return captured time from context
		val := ctx.Value(internal.KTime)
		if t, ok := val.(int64); ok {
			res = time.Unix(t, 0)
		}
	case internal.MODE_RECORD:
		res = time.Now()
	default:
		res = time.Now()
	}
	return res
}
