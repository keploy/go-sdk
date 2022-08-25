package ksql

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"

	// "github.com/keploy/go-sdk/integrations/ksql"
	"github.com/keploy/go-sdk/keploy"
	"go.keploy.io/server/pkg/models"
)

// Result is used to encode/decode driver.Result interface so that, its outputs could be stored in
// the keploy context.
type Result struct {
	LastInserted int64
	LError       string
	RowsAff      int64
	RError       string
}

func (r Result) LastInsertId() (int64, error) {
	return r.LastInserted, errors.New(r.LError)
}
func (r Result) RowsAffected() (int64, error) {
	return r.RowsAff, errors.New(r.RError)
}

// ExecContext is the mocked method of driver.ExecerContext interface. Parameters and returned variables are same as ExecContext method of database/sql/driver package.
//
// Note: ctx parameter should be the http's request context. If you are using gorm then, first call gorm.DB.WithContext(r.Context()) in your Handler function.
func (c Conn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	execerContext, ok := c.conn.(driver.ExecerContext)
	if !ok {
		return nil, errors.New("mocked Driver.Conn var not implements ExecerContext interface")
	}
	if keploy.GetModeFromContext(ctx) == keploy.MODE_OFF {
		return execerContext.ExecContext(ctx, query, args)
	}
	var (
		err          error
		kerr         *keploy.KError = &keploy.KError{}
		result       driver.Result
		driverResult *Result = &Result{}
	)
	kctx, er := keploy.GetState(ctx)
	if er != nil {
		return result, er
	}
	mode := kctx.Mode
	meta := map[string]string{
		"name":      "SQL",
		"type":      string(models.SqlDB),
		"operation": "ExecContext",
		"query":     query,
		"arguments": fmt.Sprint(args),
	}
	switch mode {
	case "test":
		//don't call Find method
	case "record":
		result, err = execerContext.ExecContext(ctx, query, args)
		if result != nil {
			// calls LastInsertId to capture their outputs
			li, e := result.LastInsertId()
			driverResult.LastInserted = li
			if e != nil {
				driverResult.LError = e.Error()
			}
			// calls RowsAffected to capture their outputs
			ra, e := result.RowsAffected()
			driverResult.RowsAff = ra
			if e != nil {
				driverResult.RError = e.Error()
			}
		}
	default:
		return result, err
	}

	if err != nil {
		kerr = &keploy.KError{Err: err}
	}

	mock, res := keploy.ProcessDep(ctx, c.log, meta, driverResult, kerr)
	if mock {
		var mockErr error
		x := res[1].(*keploy.KError)
		if x.Err != nil {
			mockErr = x.Err
		}
		mockErr = convertKError(mockErr)
		return driverResult, mockErr
	}
	return result, err
}
