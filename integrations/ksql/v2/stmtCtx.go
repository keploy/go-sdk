package ksql

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"

	"github.com/keploy/go-sdk/integrations/ksql/ksqlErr"
	"github.com/keploy/go-sdk/keploy"
	internal "github.com/keploy/go-sdk/pkg/keploy"
	proto "go.keploy.io/server/grpc/regression"
	"go.keploy.io/server/pkg/models"
)

func (c *Stmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	execContext, ok := c.Stmt.(driver.StmtExecContext)
	if !ok && internal.GetModeFromContext(ctx) != internal.MODE_TEST {
		return nil, errors.New("mocked Driver.Conn var not implements StmtExecContext interface")
	}
	if internal.GetModeFromContext(ctx) == internal.MODE_OFF {
		return execContext.ExecContext(ctx, args)
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
		"arguments": fmt.Sprint(args),
	}
	switch mode {
	case internal.MODE_TEST:
		//don't call Find method
		o, ok := MockSqlFromYaml(kctx, meta)
		if ok {
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
		result, err = execContext.ExecContext(ctx, args)
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
			}, kerr)
			// calls RowsAffected to capture their outputs
			ra, e2 := result.RowsAffected()
			driverResult.RowsAff = ra
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
			}, kerr)
		}
		return driverResult, ksqlErr.ConvertKError(err)
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
		mockErr = ksqlErr.ConvertKError(mockErr)
		return driverResult, mockErr
	}
	return result, err
}

func (c *Stmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	queryerContext, ok := c.Stmt.(driver.StmtQueryContext)
	if !ok && internal.GetModeFromContext(ctx) != internal.MODE_TEST {
		return nil, errors.New("mocked Driver.Conn var not implements StmtQueryerContext interface")
	}
	if internal.GetModeFromContext(ctx) == internal.MODE_OFF {
		return queryerContext.QueryContext(ctx, args)
	}
	var (
		err        error
		kerr       *keploy.KError = &keploy.KError{}
		driverRows *Rows          = &Rows{
			ctx:     ctx,
			args:    args,
			log:     c.log,
			columns: []*proto.SqlCol{},
			rows:    []string{},
			err:     []string{},
		}
		rows driver.Rows
	)
	kctx, er := internal.GetState(ctx)
	if er != nil {
		return nil, er
	}
	meta := map[string]string{
		"name":      "SQL",
		"type":      string(models.SqlDB),
		"operation": "QueryContext",
		"arguments": fmt.Sprint(args),
	}
	mode := kctx.Mode
	switch mode {
	case internal.MODE_TEST:
		// don't run
		driverRows.columns = nil
		driverRows.rows = nil
		driverRows.err = nil
		o, ok := MockSqlFromYaml(kctx, meta)
		if ok {
			if len(o.Err) > 0 && o.Err[0] == "nil" {
				meta1 := cloneMap(meta)
				meta1["operation"] = "QueryContext.Close"
				o1, ok1 := MockSqlFromYaml(kctx, meta1)
				if ok1 && o1.Table != nil {
					driverRows.columns = o1.Table.Cols
					driverRows.rows = o1.Table.Rows
					driverRows.err = o1.Err
				}
			}
			return driverRows, ksqlErr.ConvertKError(errors.New(o.Err[0]))
		}
	case internal.MODE_RECORD:
		rows, err = queryerContext.QueryContext(ctx, args)
		driverRows.Rows = rows
		errStr := "nil"
		if err != nil {
			kerr = &keploy.KError{Err: err}
			errStr = err.Error()
		}
		CaptureSqlMocks(kctx, c.log, meta, string(models.ErrType), sqlOutput{
			Err: []string{errStr},
		}, kerr)
		return driverRows, err
	default:
		return nil, errors.New("integrations: Not in a valid sdk mode")
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

		return driverRows, mockErr
	}
	return driverRows, err
}
