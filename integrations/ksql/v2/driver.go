package ksql

import (
	"context"
	"database/sql/driver"
	"errors"

	"github.com/keploy/go-sdk/integrations/ksql/ksqlErr"
	internal "github.com/keploy/go-sdk/internal/keploy"
	"github.com/keploy/go-sdk/keploy"
	"go.keploy.io/server/pkg/models"
	"go.uber.org/zap"
)

// Driver wraps the sql driver to overrides Open method of driver.Driver.
type Driver struct {
	driver.Driver
}

// Open returns wrapped driver.Conn in order to mock outputs of sql Querries.
//
// dsn is a string in driver specific format used as connection URI.
func (ksql *Driver) Open(dsn string) (driver.Conn, error) {
	logger, _ := zap.NewProduction()
	defer func() {
		_ = logger.Sync() // flushes buffer, if any
	}()
	var (
		res Conn
		err error
	)
	conn, err := ksql.Driver.Open(dsn)

	// if ksql.Mode == keploy.MODE_TEST {
	if internal.GetMode() == internal.MODE_TEST {
		err = nil
		conn = Conn{
			log: logger,
		}
	}
	if err != nil {
		return nil, err
	}
	res = Conn{conn: conn, log: logger} // mode: ksql.Mode

	return res, err
}

// Conn is used to override driver.Conn interface methods to mock the outputs of the queries.
type Conn struct {
	// mode string
	conn driver.Conn
	log  *zap.Logger
}

func (c Conn) Begin() (driver.Tx, error) {
	// if c.mode == keploy.MODE_TEST {
	if internal.GetMode() == internal.MODE_TEST {
		return Tx{}, nil
	}
	return c.conn.Begin()
}

func (c Conn) Close() error {
	// if c.mode == keploy.MODE_TEST {
	if internal.GetMode() == internal.MODE_TEST {

		return nil
	}
	return c.conn.Close()
}

func (c Conn) Prepare(query string) (driver.Stmt, error) {
	// if c.mode == keploy.MODE_TEST {
	if internal.GetMode() == internal.MODE_TEST {

		return &Stmt{}, nil
	}
	return c.conn.Prepare(query)
}

func (c Conn) OpenConnector(name string) (driver.Connector, error) {
	dc, ok := c.conn.(driver.DriverContext)
	if ok {
		return dc.OpenConnector(name)
	}
	return nil, errors.New("mocked Driver.Conn var not implements DriverContext interface")
}

// Ping is the mocked method of sql/driver's Ping.
func (c Conn) Ping(ctx context.Context) error {
	pc, ok := c.conn.(driver.Pinger)
	if !ok && internal.GetMode() != internal.MODE_TEST {
		return errors.New("returned var not implements Ping interface")
	}
	if internal.GetModeFromContext(ctx) == internal.MODE_OFF {
		return pc.Ping(ctx)
	}
	var (
		err  error
		kerr *keploy.KError = &keploy.KError{}
	)
	kctx, er := internal.GetState(ctx)
	if er != nil {
		return er
	}
	meta := map[string]string{
		"name":      "SQL",
		"type":      string(models.SqlDB),
		"operation": "Ping",
	}
	mode := kctx.Mode
	switch mode {
	case internal.MODE_TEST:
		// don't run
		o, ok := MockSqlFromYaml(kctx, meta)
		if ok {
			return ksqlErr.ConvertKError(errors.New(o.Err[0]))
		}
	case internal.MODE_RECORD:
		err = pc.Ping(ctx)
		errStr := "nil"
		if err != nil {
			kerr = &keploy.KError{Err: err}
			errStr = err.Error()
		}
		CaptureSqlMocks(kctx, c.log, meta, string(models.ErrType), sqlOutput{
			Err: []string{errStr},
		}, kerr)
		return err
	default:
		return errors.New("integrations: Not in a valid sdk mode")
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
		return mockErr
	}
	return err
}
