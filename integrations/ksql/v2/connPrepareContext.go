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
	"go.uber.org/zap"
)

// Stmt wraps the driver.Stmt to mock its method's outputs.
type Stmt struct {
	driver.Stmt
	ctx   context.Context
	query string
	log   *zap.Logger
}

func (s *Stmt) Exec(args []driver.Value) (driver.Result, error) {
	if internal.GetModeFromContext(s.ctx) == internal.MODE_OFF {
		return s.Stmt.Exec(args)
	}
	var (
		err      error
		kerr     *keploy.KError = &keploy.KError{}
		result   driver.Result
		drResult *Result = &Result{}
	)
	kctx, er := internal.GetState(s.ctx)
	if er != nil {
		return nil, er
	}
	mode := kctx.Mode
	meta := map[string]string{
		"name":      "SQL",
		"type":      string(models.SqlDB),
		"operation": "PrepareContext.Exec",
		"query":     fmt.Sprintf(`"%v"`, s.query),
		"arguments": fmt.Sprint(args),
	}
	switch mode {
	case internal.MODE_TEST:
		//don't call method
	case internal.MODE_RECORD:
		if s.Stmt != nil {
			result, err = s.Stmt.Exec(args)
			if result != nil {
				l, e := result.LastInsertId()
				drResult.LastInserted = l
				if e != nil {
					drResult.LError = e.Error()
				}
				ra, e := result.RowsAffected()
				drResult.RowsAff = ra
				if e != nil {
					drResult.RError = e.Error()
				}
			}
		}
	default:
		return nil, err
	}

	if err != nil {
		kerr = &keploy.KError{Err: err}
	}

	mock, res := keploy.ProcessDep(s.ctx, s.log, meta, drResult, kerr)
	if mock {
		var mockErr error
		x := res[1].(*keploy.KError)
		if x.Err != nil {
			mockErr = x.Err
		}
		mockErr = ksqlErr.ConvertKError(mockErr)
		return drResult, mockErr
	}
	return result, err
}

func (s *Stmt) Query(args []driver.Value) (driver.Rows, error) {
	if internal.GetModeFromContext(s.ctx) == internal.MODE_OFF {
		return s.Stmt.Query(args)
	}
	var (
		err    error
		kerr   *keploy.KError = &keploy.KError{}
		drRows *Rows          = &Rows{
			ctx:   s.ctx,
			query: s.query,
		}
		rows driver.Rows
	)
	kctx, er := internal.GetState(s.ctx)
	if er != nil {
		return nil, er
	}
	mode := kctx.Mode
	switch mode {
	case internal.MODE_TEST:
		// don't run
	case internal.MODE_RECORD:
		if s.Stmt != nil {
			rows, err = s.Stmt.Query(args)
			drRows.Rows = rows
		}
	default:
		return nil, errors.New("integrations: Not in a valid sdk mode")
	}
	meta := map[string]string{
		"name":      "SQL",
		"type":      string(models.SqlDB),
		"operation": "PrepareContext.Query",
		"query":     fmt.Sprintf(`"%v"`, s.query),
		"arguments": fmt.Sprint(args),
	}
	if err != nil {
		kerr = &keploy.KError{Err: err}
	}
	mock, res := keploy.ProcessDep(s.ctx, s.log, meta, kerr)
	if mock {
		var mockErr error
		x := res[0].(*keploy.KError)
		if x.Err != nil {
			mockErr = x.Err
		}
		mockErr = ksqlErr.ConvertKError(mockErr)
		return drRows, mockErr
	}
	return drRows, err
}

func (s *Stmt) NumInput() int {
	if internal.GetModeFromContext(s.ctx) == internal.MODE_OFF {
		return s.Stmt.NumInput()
	}
	var (
		x      int            = 1
		output *int           = &x
		kerr   *keploy.KError = &keploy.KError{}
	)
	kctx, er := internal.GetState(s.ctx)
	if er != nil {
		return 0
	}
	meta := map[string]string{
		"name":      "SQL",
		"type":      string(models.SqlDB),
		"operation": "PrepareContext.NumInput",
	}
	mode := kctx.Mode
	switch mode {
	case internal.MODE_TEST:
		// don't run
		o, ok := MockSqlFromYaml(kctx, meta)
		if ok {
			return o.Count
		}
	case internal.MODE_RECORD:
		if s.Stmt != nil {
			o := s.Stmt.NumInput()
			output = &o
		}
		CaptureSqlMocks(kctx, s.log, meta, string(models.IntType), sqlOutput{
			Count: *output,
			Err:   []string{"nil"},
		}, output, kerr)
		return *output
	default:
		return 0
	}
	mock, _ := keploy.ProcessDep(s.ctx, s.log, meta, output, kerr)
	if mock {
		return *output
	}
	return *output
}

func (s *Stmt) Close() error {
	if internal.GetModeFromContext(s.ctx) == internal.MODE_OFF {
		return s.Stmt.Close()
	}
	var (
		err  error
		kerr *keploy.KError = &keploy.KError{}
	)
	kctx, er := internal.GetState(s.ctx)
	if er != nil {
		return er
	}
	meta := map[string]string{
		"name":      "SQL",
		"type":      string(models.SqlDB),
		"operation": "PrepareContext.Close",
	}
	mode := kctx.Mode
	switch mode {
	case internal.MODE_TEST:
		// don't run
		o, ok := MockSqlFromYaml(kctx, meta)
		if ok && len(o.Err) == 1 {
			return ksqlErr.ConvertKError(errors.New(o.Err[0]))
		}
	case internal.MODE_RECORD:
		if s.Stmt != nil {
			err = s.Stmt.Close()
		}
		errStr := "nil"
		if err != nil {
			errStr = err.Error()
			kerr = &keploy.KError{Err: err}
		}
		CaptureSqlMocks(kctx, s.log, meta, string(models.ErrType), sqlOutput{
			Err: []string{errStr},
		}, kerr)
		return err
	default:
		return errors.New("integrations: Not in a valid sdk mode")
	}
	if err != nil {
		kerr = &keploy.KError{Err: err}
	}
	mock, res := keploy.ProcessDep(s.ctx, s.log, meta, kerr)
	if mock {
		var mockErr error
		x := res[0].(*keploy.KError)
		if x.Err != nil {
			mockErr = x.Err
		}
		mockErr = ksqlErr.ConvertKError(mockErr)
		return mockErr
	}
	return err
}

// PrepareContext mocks the outputs of PrepareContext method of sql/driver's ConnPrepareContext interface.
func (c Conn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	pc, ok := c.conn.(driver.ConnPrepareContext)
	if !ok {
		return nil, errors.New("returned var not implements PrepareContext interface")
	}
	var (
		err    error
		kerr   *keploy.KError = &keploy.KError{}
		drStmt *Stmt          = &Stmt{
			log:   c.log,
			ctx:   ctx,
			query: query,
		}
		stmt driver.Stmt
	)
	if internal.GetModeFromContext(ctx) == internal.MODE_OFF {
		stmt, err = pc.PrepareContext(ctx, query)
		drStmt.Stmt = stmt
		return drStmt, err
	}
	kctx, er := internal.GetState(ctx)
	if er != nil {
		return nil, er
	}
	mode := kctx.Mode
	meta := map[string]string{
		"name":      "SQL",
		"type":      string(models.SqlDB),
		"operation": "PrepareContext",
		"query":     fmt.Sprintf(`"%v"`, query),
	}
	switch mode {
	case internal.MODE_TEST:
		// mocks of SQL kind
		o, ok := MockSqlFromYaml(kctx, meta)
		if ok && len(o.Err) == 1 {
			return drStmt, ksqlErr.ConvertKError(errors.New(o.Err[0]))
		}
	case internal.MODE_RECORD:
		stmt, err = pc.PrepareContext(ctx, query)
		drStmt.Stmt = stmt
		errStr := "nil"
		if err != nil {
			errStr = err.Error()
			kerr = &keploy.KError{Err: err}
		}
		CaptureSqlMocks(kctx, c.log, meta, string(models.ErrType), sqlOutput{
			Err: []string{errStr},
		}, kerr)
		return drStmt, err
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
		return drStmt, ksqlErr.ConvertKError(mockErr)
	}
	return drStmt, err
}
