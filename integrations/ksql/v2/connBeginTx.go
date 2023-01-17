package ksql

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"

	// v2 "github.com/keploy/go-sdk/integrations/ksql/v2"
	"github.com/keploy/go-sdk/integrations/ksql/ksqlErr"
	internal "github.com/keploy/go-sdk/internal/keploy"
	"github.com/keploy/go-sdk/keploy"
	"go.keploy.io/server/pkg/models"
	"go.uber.org/zap"
)

// Tx wraps driver.Tx to mock its methods output.
type Tx struct {
	driver.Tx
	ctx context.Context
	log *zap.Logger
}

// Commit mocks the outputs of Commit method present driver's Tx interface.
func (t Tx) Commit() error {
	if internal.GetModeFromContext(t.ctx) == internal.MODE_OFF {
		return t.Tx.Commit()
	}
	var (
		err  error
		kerr *keploy.KError = &keploy.KError{}
	)
	kctx, er := internal.GetState(t.ctx)
	if er != nil {
		return er
	}
	meta := map[string]string{
		"name":      "SQL",
		"type":      string(models.SqlDB),
		"operation": "BeginTx.Commit",
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
		if t.Tx != nil {
			err = t.Tx.Commit()
		}
		errStr := "nil"
		if err != nil {
			kerr = &keploy.KError{Err: err}
			errStr = err.Error()
		}
		CaptureSqlMocks(kctx, t.log, meta, string(models.ErrType), sqlOutput{
			Err: []string{errStr},
		}, kerr)
		return err
	default:
		return errors.New("integrations: Not in a valid sdk mode")
	}
	if err != nil {
		kerr = &keploy.KError{Err: err}
	}
	mock, res := keploy.ProcessDep(t.ctx, t.log, meta, kerr)
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

// Rollback mocks the outputs of Rollback method present driver's Tx interface.
func (t Tx) Rollback() error {
	if internal.GetModeFromContext(t.ctx) == internal.MODE_OFF {
		return t.Tx.Rollback()
	}
	var (
		err  error
		kerr *keploy.KError = &keploy.KError{}
	)
	kctx, er := internal.GetState(t.ctx)
	if er != nil {
		return er
	}
	meta := map[string]string{
		"name":      "SQL",
		"type":      string(models.SqlDB),
		"operation": "BeginTx.Rollback",
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
		if t.Tx != nil {
			err = t.Tx.Rollback()
		}
		errStr := "nil"
		if err != nil {
			kerr = &keploy.KError{Err: err}
			errStr = err.Error()
		}
		CaptureSqlMocks(kctx, t.log, meta, string(models.ErrType), sqlOutput{
			Err: []string{errStr},
		}, kerr)
		return err
	default:
		return errors.New("integrations: Not in a valid sdk mode")
	}
	if err != nil {
		kerr = &keploy.KError{Err: err}
	}
	mock, res := keploy.ProcessDep(t.ctx, t.log, meta, kerr)
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

// BeginTx mocks the encoded outputs of BeginTx in test mode and captures encoded outputs in capture mode.
func (c Conn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	bc, ok := c.conn.(driver.ConnBeginTx)
	if !ok {
		return nil, errors.New("returned var not implements ConnBeginTx interface")
	}
	if internal.GetModeFromContext(ctx) == internal.MODE_OFF {
		return bc.BeginTx(ctx, opts)
	}
	var (
		err  error
		kerr *keploy.KError = &keploy.KError{}
		tx   driver.Tx
		drTx *Tx = &Tx{
			log: c.log,
			ctx: ctx,
		}
	)
	kctx, er := internal.GetState(ctx)
	if er != nil {
		return nil, er
	}
	meta := map[string]string{
		"name":      "SQL",
		"type":      string(models.SqlDB),
		"operation": "BeginTx",
		"options":   fmt.Sprint(opts),
	}
	mode := kctx.Mode
	switch mode {
	case internal.MODE_TEST:
		// don't run
		o, ok := MockSqlFromYaml(kctx, meta)
		if ok && len(o.Err) == 1 {
			return drTx, ksqlErr.ConvertKError(errors.New(o.Err[0]))
		}
	case internal.MODE_RECORD:
		tx, err = bc.BeginTx(ctx, opts)
		drTx.Tx = tx
		errStr := "nil"
		if err != nil {
			kerr = &keploy.KError{Err: err}
			errStr = err.Error()
		}
		CaptureSqlMocks(kctx, c.log, meta, string(models.ErrType), sqlOutput{
			Err: []string{errStr},
		}, kerr)
		return drTx, err
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
		return drTx, mockErr
	}
	return drTx, err
}
