package ksql

import (
	"context"
	"database/sql/driver"
	// "encoding/gob"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"
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
	var (
		res Conn
		err error
	)
	conn, err := ksql.Driver.Open(dsn)
	if err != nil {
		return nil, err
	}
	logger, _ := zap.NewProduction()
	defer func() {
		_ = logger.Sync() // flushes buffer, if any
	}()
	res = Conn{conn: conn, log: logger}
	return res, err
}

// Conn is used to override driver.Conn interface methods to mock the outputs of the querries. 
type Conn struct {
	conn driver.Conn
	log *zap.Logger
}

func (c Conn) Begin() (driver.Tx, error) {
	return c.conn.Begin()
}

func (c Conn) Close() error {
	return c.conn.Close()
}

func (c Conn) Prepare(query string) (driver.Stmt, error) {
	return c.conn.Prepare(query)
}

func (c Conn) OpenConnector(name string) (driver.Connector, error) {
	dc, ok := c.conn.(driver.DriverContext)
	if ok {
		return dc.OpenConnector(name)
	}
	return nil, errors.New("mocked Driver.Conn var not implements DriverContext interface")
}

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
		err      error
		kerr     *keploy.KError = &keploy.KError{}
		result   driver.Result
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
	case "capture":
		result, err = execerContext.ExecContext(ctx, query, args)
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
		return driverResult, mockErr
	}
	return result, err
}

// Rows mocks the driver.Rows methods to store their encoded outputs.
type Rows struct {
	ctx context.Context
	driver.Rows
	query string
	args  []driver.NamedValue
	log *zap.Logger
}
// Columns mocks the output of Columns method of your SQL driver.
func (r Rows) Columns() []string {
	if keploy.GetModeFromContext(r.ctx) == keploy.MODE_OFF {
		return r.Rows.Columns()
	}
	var (
		output *[]string = &[]string{}
	)
	kctx, er := keploy.GetState(r.ctx)
	if er != nil {
		return nil
	}
	mode := kctx.Mode
	switch mode {
	case "test":
		// don't run
	case "capture":
		o := r.Rows.Columns()
		output = &o
	default:
		return nil
	}
	meta := map[string]string{
		"name":      "SQL",
		"type":      string(models.SqlDB),
		"operation": "QueryContext.Columns",
	}
	
	mock, _ := keploy.ProcessDep(r.ctx, r.log, meta, output)
	if mock {
		return *output
	}
	return *output
}
// Close mocks the output of Close method of your SQL driver.
func (r Rows) Close() error {
	if keploy.GetModeFromContext(r.ctx) == keploy.MODE_OFF {
		return r.Rows.Close()
	}
	var (
		err  error
		kerr *keploy.KError = &keploy.KError{}
	)
	kctx, er := keploy.GetState(r.ctx)
	if er != nil {
		return er
	}
	mode := kctx.Mode
	switch mode {
	case "test":
		// don't run
	case "capture":
		err = r.Rows.Close()
	default:
		return errors.New("integrations: Not in a valid sdk mode")
	}
	meta := map[string]string{
		"name":      "SQL",
		"type":      string(models.SqlDB),
		"operation": "QueryContext.Close",
	}
	if err != nil {
		kerr = &keploy.KError{Err: err}
	}
	
	mock, res := keploy.ProcessDep(r.ctx, r.log, meta, kerr)
	if mock {
		var mockErr error
		x := res[0].(*keploy.KError)
		if x.Err != nil {
			mockErr = x.Err
		}
		return mockErr
	}
	return err
}

// Value wraps the Value from the sql driver to encode/decode using gob.
type Value struct {
	Value []driver.Value
}

// GobEncode encodes the Value using gob Encoder.
func (d *Value) GobEncode() ([]byte, error) {
	if d.Value == nil {
		return []byte{}, nil
	}
	res := make([][]byte, 0)

	for _, j := range d.Value {
		el := make([]byte, 0)
		x := reflect.ValueOf(j)
		switch x.Kind() {
		case reflect.Float64:
			el = append(el, 1)
		case reflect.Int64:
			el = append(el, 2)
		case reflect.String:
			el = append(el, 3)
		case reflect.Bool:
			el = append(el, 4)
		default:
			if _, ok := j.(time.Time); ok {
				el = append(el, 5)
			} else if _, ok := j.([]byte); ok {
				el = append(el, 6)
			} else {
				el = append(el, 7)
			}
		}
		r, err := json.Marshal(j)
		if err != nil {
			return nil, err
		}
		el = append(el, r...)
		res = append(res, el)
	}
	return json.Marshal(res)

}
// GobDecode decodes Values using gob Decoder.
func (d *Value) GobDecode(b []byte) error {
	var res *[][]byte = &[][]byte{}
	value := []driver.Value{}
	err := json.Unmarshal(b, res)
	if err != nil {
		return err
	}
	for _, j := range *res {
		switch j[0] {
		case 1:
			f := 1.1
			var x *float64 = &f
			err = json.Unmarshal(j[1:], x)
			if err != nil {
				return err
			}
			value = append(value, *x)
		case 2:
			var in int64 = 1
			var x *int64 = &in
			err = json.Unmarshal(j[1:], x)
			if err != nil {
				return err
			}
			value = append(value, *x)
		case 3:
			s := "d"
			var x *string = &s
			err = json.Unmarshal(j[1:], x)
			if err != nil {
				return err
			}
			value = append(value, *x)
		case 4:
			var in bool = true
			var x *bool = &in
			err = json.Unmarshal(j[1:], x)
			if err != nil {
				return err
			}
			value = append(value, *x)
		case 5:
			in := time.Time{}
			var x *time.Time = &in
			err = json.Unmarshal(j[1:], x)
			if err != nil {
				return err
			}
			value = append(value, *x)
		case 6:
			in := []byte{}
			var x *[]byte = &in
			err = json.Unmarshal(j[1:], x)
			if err != nil {
				return err
			}
			value = append(value, *x)
		case 7:
			value = append(value, nil)
		default:
			return errors.New("failed to decode")
		}
	}
	d.Value = value
	return nil
}

// Next mocks the outputs of Next method of sql Driver.
func (r Rows) Next(dest []driver.Value) error {
	if keploy.GetModeFromContext(r.ctx) == keploy.MODE_OFF {
		return r.Rows.Next(dest)
	}
	var (
		err    error
		kerr   *keploy.KError = &keploy.KError{}
		output *Value   = &Value{Value: dest}
	)
	kctx, er := keploy.GetState(r.ctx)
	if er != nil {
		return er
	}
	mode := kctx.Mode
	switch mode {
	case "test":
		// don't run
	case "capture":
		err = r.Rows.Next(dest)
		output.Value = dest
	default:
		return errors.New("integrations: Not in a valid sdk mode")
	}
	meta := map[string]string{
		"name":      "SQL",
		"type":      string(models.SqlDB),
		"operation": "QueryContext.Next",
	}
	if err != nil {
		kerr = &keploy.KError{Err: err}
	}
	mock, res := keploy.ProcessDep(r.ctx, r.log, meta, kerr, output)
	if mock {
		var mockErr error
		x := res[0].(*keploy.KError)
		if x.Err != nil {
			mockErr = x.Err
		}
		for i := 0; i < len(dest); i++ {
			if i < len(output.Value) {
				dest[i] = output.Value[i]
			}
		}
		return mockErr
	}
	return err
}

// QueryContext mocks the outputs of QueryContext method of sql driver's QueryerContext interface.
func (c Conn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	queryerContext, ok := c.conn.(driver.QueryerContext)
	if !ok {
		return nil, errors.New("returned var not implements QueryerContext interface")
	}
	if keploy.GetModeFromContext(ctx) == keploy.MODE_OFF {
		return queryerContext.QueryContext(ctx, query, args)
	}
	var (
		err    error
		kerr   *keploy.KError = &keploy.KError{}
		driverRows *Rows    = &Rows{
			ctx   :   ctx,
			query :   query,
			args  :   args,
			log   :   c.log,
		}
		rows driver.Rows
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
		rows, err = queryerContext.QueryContext(ctx, query, args)
		driverRows.Rows = rows
	default:
		return nil, errors.New("integrations: Not in a valid sdk mode")
	}
	meta := map[string]string{
		"name":      "SQL",
		"type":      string(models.SqlDB),
		"operation": "QueryContext",
		"query":     query,
		"arguments": fmt.Sprint(args),
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
		return driverRows, mockErr
	}
	return driverRows, err
}

// Stmt wraps the driver.Stmt to mock its method's outputs.
type Stmt struct {
	driver.Stmt
	ctx   context.Context
	query string
	log *zap.Logger
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
		"query":     s.query,
		"arguments": fmt.Sprint(args),
	}
	switch mode {
	case "test":
		//don't call method
	case "capture":
		result, err = s.Stmt.Exec(args)
		l, e := result.LastInsertId()
		drResult.LastInserted = l
		drResult.LError = e.Error()
		ra, e := result.RowsAffected()
		drResult.RowsAff = ra
		drResult.RError = e.Error()
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
		drRows *Rows    = &Rows{
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
	case "test":
		// don't run
	case "capture":
		rows, err = s.Stmt.Query(args)
		drRows.Rows = rows
	default:
		return nil, errors.New("integrations: Not in a valid sdk mode")
	}
	meta := map[string]string{
		"name":      "SQL",
		"type":      string(models.SqlDB),
		"operation": "PrepareContext.Query",
		"query":     s.query,
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
	case "test":
		// don't run
	case "capture":
		o := s.Stmt.NumInput()
		output = &o
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
	case "test":
		// don't run
	case "capture":
		err = s.Stmt.Close()
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
		drStmt *Stmt    = &Stmt{
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
	case "test":
		// don't run
	case "capture":
		stmt, err = pc.PrepareContext(ctx, query)
		drStmt.Stmt = stmt
	default:
		return nil, errors.New("integrations: Not in a valid sdk mode")
	}
	meta := map[string]string{
		"name":      "SQL",
		"type":      string(models.SqlDB),
		"operation": "PrepareContext",
		"query":     query,
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
		return drStmt, mockErr
	}
	return drStmt, err
}

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
		return drTx, mockErr
	}
	return drTx, err
}

// Ping is the mocked method of sql/driver's Ping. 
func (c Conn) Ping(ctx context.Context) error {
	pc, ok := c.conn.(driver.Pinger)
	if !ok {
		return errors.New("returned var not implements ConnBeginTx interface")
	}
	// return pc.Ping(ctx)
	if keploy.GetModeFromContext(ctx) == keploy.MODE_OFF {
		return pc.Ping(ctx)
	}
	var (
		err  error
		kerr *keploy.KError = &keploy.KError{}
	)
	kctx, er := keploy.GetState(ctx)
	if er != nil {
		return er
	}
	mode := kctx.Mode
	switch mode {
	case "test":
		// don't run
	case "capture":
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
		return mockErr
	}
	return err
}
