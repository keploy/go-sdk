package ksql

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"

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

func (s Stmt) Exec(args []driver.Value) (driver.Result, error) {
	if keploy.GetModeFromContext(s.ctx) == keploy.MODE_OFF {
		return s.Stmt.Exec(args)
	}
	var (
		err      error
		kerr     *keploy.KError = &keploy.KError{}
		result   driver.Result
		drResult *Result = &Result{}
	)
	kctx, er := keploy.GetState(s.ctx)
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
	case keploy.MODE_TEST:
		//don't call method
	case keploy.MODE_RECORD:
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
		mockErr = convertKError(mockErr)
		return drResult, mockErr
	}
	return result, err
}
func (s Stmt) Query(args []driver.Value) (driver.Rows, error) {
	if keploy.GetModeFromContext(s.ctx) == keploy.MODE_OFF {
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
	kctx, er := keploy.GetState(s.ctx)
	if er != nil {
		return nil, er
	}
	mode := kctx.Mode
	switch mode {
	case keploy.MODE_TEST:
		// don't run
	case keploy.MODE_RECORD:
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
		mockErr = convertKError(mockErr)
		return drRows, mockErr
	}
	return drRows, err
}
func (s Stmt) NumInput() int {
	if keploy.GetModeFromContext(s.ctx) == keploy.MODE_OFF {
		return s.Stmt.NumInput()
	}
	var (
		x      int  = 1
		output *int = &x
	)
	kctx, er := keploy.GetState(s.ctx)
	if er != nil {
		return 0
	}
	mode := kctx.Mode
	switch mode {
	case keploy.MODE_TEST:
		// don't run
	case keploy.MODE_RECORD:
		if s.Stmt != nil {
			o := s.Stmt.NumInput()
			output = &o
		}
	default:
		return 0
	}
	meta := map[string]string{
		"name":      "SQL",
		"type":      string(models.SqlDB),
		"operation": "PrepareContext.NumInput",
	}
	mock, _ := keploy.ProcessDep(s.ctx, s.log, meta, output)
	if mock {
		return *output
	}
	return *output
}
func (s Stmt) Close() error {
	if keploy.GetModeFromContext(s.ctx) == keploy.MODE_OFF {
		return s.Stmt.Close()
	}
	var (
		err  error
		kerr *keploy.KError = &keploy.KError{}
	)
	kctx, er := keploy.GetState(s.ctx)
	if er != nil {
		return er
	}
	mode := kctx.Mode
	switch mode {
	case keploy.MODE_TEST:
		// don't run
	case keploy.MODE_RECORD:
		if s.Stmt != nil {
			err = s.Stmt.Close()
		}
	default:
		return errors.New("integrations: Not in a valid sdk mode")
	}
	meta := map[string]string{
		"name":      "SQL",
		"type":      string(models.SqlDB),
		"operation": "PrepareContext.Close",
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
		mockErr = convertKError(mockErr)
		return mockErr
	}
	return err
}

// PrepareContext mocks the outputs of PrepareContext method of sql/driver's ConnPrepareContext interface.
func (c Conn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	pc, ok := c.conn.(driver.ConnPrepareContext)
	if !ok {
		return nil, errors.New("returned var not implements QueryerContext interface")
	}
	if keploy.GetModeFromContext(ctx) == keploy.MODE_OFF {
		return pc.PrepareContext(ctx, query)
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
	kctx, er := keploy.GetState(ctx)
	if er != nil {
		return nil, er
	}
	mode := kctx.Mode
	switch mode {
	case keploy.MODE_TEST:
		// don't run
	case keploy.MODE_RECORD:
		stmt, err = pc.PrepareContext(ctx, query)
		drStmt.Stmt = stmt
	default:
		return nil, errors.New("integrations: Not in a valid sdk mode")
	}
	meta := map[string]string{
		"name":      "SQL",
		"type":      string(models.SqlDB),
		"operation": "PrepareContext",
		"query":     fmt.Sprintf(`"%v"`, query),
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
		mockErr = convertKError(mockErr)
		return drStmt, mockErr
	}
	return drStmt, err
}
