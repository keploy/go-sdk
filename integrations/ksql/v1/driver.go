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
	// Mode string
}

// Open returns wrapped driver.Conn in order to mock outputs of sql Querries.
//
// dsn is a string in driver specific format used as connection URI.
func (ksql *Driver) Open(dsn string) (driver.Conn, error) {
	var (
		res Conn
		err error
	)
	conn, err := ksql.Driver.Open(dsn)

	// if ksql.Mode == keploy.MODE_TEST {
	if keploy.GetMode() == keploy.MODE_TEST {
		err = nil
		conn = Conn{}
	}
	if err != nil {
		return nil, err
	}
	logger, _ := zap.NewProduction()
	defer func() {
		_ = logger.Sync() // flushes buffer, if any
	}()
	res = Conn{conn: conn, log: logger} // mode: ksql.Mode

	return res, err
}

// Conn is used to override driver.Conn interface methods to mock the outputs of the querries.
type Conn struct {
	// mode string
	conn driver.Conn
	log  *zap.Logger
}

func (c Conn) Begin() (driver.Tx, error) {
	// if c.mode == keploy.MODE_TEST {
	if keploy.GetMode() == keploy.MODE_TEST {

		return Tx{}, nil
	}
	return c.conn.Begin()
}

func (c Conn) Close() error {
	// if c.mode == keploy.MODE_TEST {
	if keploy.GetMode() == keploy.MODE_TEST {

		return nil
	}
	return c.conn.Close()
}

func (c Conn) Prepare(query string) (driver.Stmt, error) {
	// if c.mode == keploy.MODE_TEST {
	if keploy.GetMode() == keploy.MODE_TEST {

		return Stmt{}, nil
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
	if !ok {
		return errors.New("returned var not implements ConnBeginTx interface")
	}
	// return pc.Ping(ctx)
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
	mode := kctx.Mode
	switch mode {
	case keploy.MODE_TEST:
		// don't run
	case keploy.MODE_RECORD:
		err = pc.Ping(ctx)
	default:
		return errors.New("integrations: Not in a valid sdk mode")
	}
	meta := map[string]string{
		"name":      "SQL",
		"type":      string(models.SqlDB),
		"operation": "Ping",
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
