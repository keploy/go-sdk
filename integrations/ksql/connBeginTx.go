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

// Tx wraps driver.Tx to mock its methods output.
type Tx struct {
	driver.Tx
	ctx context.Context
	log *zap.Logger
}

// Commit mocks the outputs of Commit method present driver's Tx interface.
func (t Tx) Commit() error {
	if keploy.GetModeFromContext(t.ctx) == keploy.MODE_OFF {
		return t.Tx.Commit()
	}
	var (
		err  error
		kerr *keploy.KError = &keploy.KError{}
	)
	kctx, er := keploy.GetState(t.ctx)
	if er != nil {
		return er
	}
	mode := kctx.Mode
	switch mode {
	case "test":
		// don't run
	case "capture":
		err = t.Tx.Commit()
	default:
		return errors.New("integrations: Not in a valid sdk mode")
	}
	meta := map[string]string{
		"name":      "SQL",
		"type":      string(models.SqlDB),
		"operation": "BeginTx.Commit",
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
		mockErr = convertKError(mockErr)
		return mockErr
	}
	return err
}

// Rollback mocks the outputs of Rollback method present driver's Tx interface.
func (t Tx) Rollback() error {
	if keploy.GetModeFromContext(t.ctx) == keploy.MODE_OFF {
		return t.Tx.Rollback()
	}
	var (
		err  error
		kerr *keploy.KError = &keploy.KError{}
	)
	kctx, er := keploy.GetState(t.ctx)
	if er != nil {
		return er
	}
	mode := kctx.Mode
	switch mode {
	case "test":
		// don't run
	case "capture":
		err = t.Tx.Rollback()
	default:
		return errors.New("integrations: Not in a valid sdk mode")
	}
	meta := map[string]string{
		"name":      "SQL",
		"type":      string(models.SqlDB),
		"operation": "BeginTx.Rollback",
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
		mockErr = convertKError(mockErr)
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
	if keploy.GetModeFromContext(ctx) == keploy.MODE_OFF {
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
	kctx, er := keploy.GetState(ctx)
	if er != nil {
		return nil, er
	}
	mode := kctx.Mode
	switch mode {
	case "test":
		// don't run
	case "capture":
		tx, err = bc.BeginTx(ctx, opts)
		drTx.Tx = tx
	default:
		return nil, errors.New("integrations: Not in a valid sdk mode")
	}
	if err != nil {
		kerr = &keploy.KError{Err: err}
	}
	meta := map[string]string{
		"name":      "SQL",
		"type":      string(models.SqlDB),
		"operation": "BeginTx",
		"options":   fmt.Sprint(opts),
	}
	mock, res := keploy.ProcessDep(ctx, c.log, meta, kerr)
	if mock {
		var mockErr error
		x := res[0].(*keploy.KError)
		if x.Err != nil {
			mockErr = x.Err
		}
		mockErr = convertKError(mockErr)
		return drTx, mockErr
	}
	return drTx, err
}
