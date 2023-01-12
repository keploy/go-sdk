package ksql

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"

	"github.com/keploy/go-sdk/integrations/ksql/ksqlErr"
	internal "github.com/keploy/go-sdk/internal/keploy"
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
	return r.LastInserted, ksqlErr.ConvertKError(errors.New(r.LError))
}
func (r Result) RowsAffected() (int64, error) {
	return r.RowsAff, ksqlErr.ConvertKError(errors.New(r.RError))
}

// ExecContext is the mocked method of driver.ExecerContext interface. Parameters and returned variables are same as ExecContext method of database/sql/driver package.
//
// Note: ctx parameter should be the http's request context. If you are using gorm then, first call gorm.DB.WithContext(r.Context()) in your Handler function.
func (c Conn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	execerContext, _ := c.conn.(driver.ExecerContext)
	// if !ok {
	// 	return nil, errors.New("mocked Driver.Conn var not implements ExecerContext interface")
	// }
	fmt.Println(" Called ExecContext with mode: ", internal.GetModeFromContext(ctx))
	if internal.GetModeFromContext(ctx) == internal.MODE_OFF {
		return execerContext.ExecContext(ctx, query, args)
	}
	var (
		err          error
		kerr         *keploy.KError = &keploy.KError{}
		result       driver.Result
		driverResult *Result = &Result{LError: "nil", RError: "nil"}
	)
	kctx, er := internal.GetState(ctx)
	if er != nil {
		return result, er
	}
	mode := kctx.Mode
	meta := map[string]string{
		"name":      "SQL",
		"type":      string(models.SqlDB),
		"operation": "ExecContext",
		"query":     fmt.Sprintf(`"%v"`, query),
		"arguments": fmt.Sprint(args),
	}
	switch mode {
	case internal.MODE_TEST:
		//don't call Find method
		o, ok := MockSqlFromYaml(kctx, meta)
		if ok && len(o.Err) == 1 {
			meta1 := cloneMap(meta)
			meta1["operation"] = "ExecContext.LastInsertId"
			o1, ok1 := MockSqlFromYaml(kctx, meta1)
			if ok1 && len(o1.Err) == 1 {
				driverResult.LastInserted = int64(o1.Count)
				driverResult.LError = o1.Err[0]
			}
			meta2 := cloneMap(meta)
			meta2["operation"] = "ExecContext.RowsAffected"
			o2, ok2 := MockSqlFromYaml(kctx, meta2)
			if ok2 && len(o2.Err) == 1 {
				driverResult.RowsAff = int64(o2.Count)
				driverResult.RError = o2.Err[0]
			}
			return driverResult, ksqlErr.ConvertKError(errors.New(o.Err[0]))
		}
	case internal.MODE_RECORD:
		result, err = execerContext.ExecContext(ctx, query, args)
		errStr := "nil"
		if err != nil {
			errStr = err.Error()
			kerr = &keploy.KError{Err: err}
		}
		CaptureSqlMocks(kctx, c.log, meta, string(models.ErrType), sqlOutput{
			Err: []string{errStr},
		}, kerr)
		if result != nil {
			// calls LastInsertId to capture their outputs
			li, e1 := result.LastInsertId()
			output1 := &li
			driverResult.LastInserted = li
			kerr = &keploy.KError{}
			meta1 := cloneMap(meta)
			meta1["operation"] = "ExecContext.LastInsertId"
			if e1 != nil {
				driverResult.LError = e1.Error()
				kerr = &keploy.KError{Err: e1}
			}
			CaptureSqlMocks(kctx, c.log, meta1, string(models.IntType), sqlOutput{
				Count: int(li),
				Err:   []string{driverResult.LError},
			}, output1, kerr)
			// calls RowsAffected to capture their outputs
			ra, e2 := result.RowsAffected()
			driverResult.RowsAff = ra
			output2 := &ra
			kerr = &keploy.KError{}
			meta2 := cloneMap(meta)
			meta2["operation"] = "ExecContext.RowsAffected"
			if e2 != nil {
				driverResult.RError = e2.Error()
				kerr = &keploy.KError{Err: e2}
			}
			CaptureSqlMocks(kctx, c.log, meta2, string(models.IntType), sqlOutput{
				Count: int(ra),
				Err:   []string{driverResult.RError},
			}, output2, kerr)
		}
		return driverResult, ksqlErr.ConvertKError(err)
	default:
		return result, err
	}

	if err != nil {
		kerr = &keploy.KError{Err: err}
	}

	mock, res := keploy.ProcessDep(ctx, c.log, meta, kerr)
	if mock {
		var mockErr error
		x := res[0].(*keploy.KError)
		if x.Err != nil {
			mockErr = x.Err
		}
		mockErr = ksqlErr.ConvertKError(mockErr)
		var (
			z       int64          = 0
			y       int64          = 0
			output1 *int64         = &z
			output2 *int64         = &y
			kerr1   *keploy.KError = &keploy.KError{}
			kerr2   *keploy.KError = &keploy.KError{}
		)
		mock1, _ := keploy.ProcessDep(ctx, c.log, meta, output1, kerr1)
		if mock1 {
			// x := res1[1].(*keploy.KError)
			if kerr1.Err != nil {
				driverResult.LError = kerr1.Err.Error()
			}
			driverResult.LastInserted = *output1
		}

		mock2, _ := keploy.ProcessDep(ctx, c.log, meta, output2, kerr2)
		if mock2 {
			// x := res2[1].(*keploy.KError)
			if kerr2.Err != nil {
				driverResult.RError = kerr2.Err.Error()
			}
			driverResult.RowsAff = *output2
		}

		return driverResult, mockErr
	}
	return result, err
}
