package ksql

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/keploy/go-sdk/integrations/ksql/ksqlErr"
	internal "github.com/keploy/go-sdk/internal/keploy"
	"github.com/keploy/go-sdk/keploy"
	"go.keploy.io/server/pkg/models"
	"go.uber.org/zap"
)

// Rows mocks the driver.Rows methods to store their encoded outputs.
type Rows struct {
	ctx context.Context
	driver.Rows
	query string
	args  []driver.NamedValue
	log   *zap.Logger
}

// Columns mocks the output of Columns method of your SQL driver.
func (r Rows) Columns() []string {
	if internal.GetModeFromContext(r.ctx) == internal.MODE_OFF {
		return r.Rows.Columns()
	}
	var (
		output *[]string = &[]string{}
	)
	kctx, er := internal.GetState(r.ctx)
	if er != nil {
		return nil
	}
	mode := kctx.Mode
	switch mode {
	case keploy.MODE_TEST:
		// don't run
	case keploy.MODE_RECORD:
		if r.Rows != nil {
			o := r.Rows.Columns()
			output = &o
		}
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
	if internal.GetModeFromContext(r.ctx) == internal.MODE_OFF {
		return r.Rows.Close()
	}
	var (
		err  error
		kerr *keploy.KError = &keploy.KError{}
	)
	kctx, er := internal.GetState(r.ctx)
	if er != nil {
		return er
	}
	mode := kctx.Mode
	switch mode {
	case keploy.MODE_TEST:
		// don't run actual rows.Close
		// ignore the rows.Close which is not done manually.
		if kctx.Deps == nil || len(kctx.Deps) == 0 || len(kctx.Deps[0].Data) != 1 {
			return nil
		}
	case keploy.MODE_RECORD:
		if r.Rows != nil {
			err = r.Rows.Close()
		}
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
		mockErr = ksqlErr.ConvertKError(mockErr)
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
	if internal.GetModeFromContext(r.ctx) == internal.MODE_OFF {
		return r.Rows.Next(dest)
	}
	var (
		err    error
		kerr   *keploy.KError = &keploy.KError{}
		output *Value         = &Value{Value: dest}
	)
	kctx, er := internal.GetState(r.ctx)
	if er != nil {
		return er
	}
	mode := kctx.Mode
	switch mode {
	case keploy.MODE_TEST:
		// don't run
	case keploy.MODE_RECORD:
		if r.Rows != nil {
			err = r.Rows.Next(dest)
			output.Value = dest
		}
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
		mockErr = ksqlErr.ConvertKError(mockErr)
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
	if internal.GetModeFromContext(ctx) == internal.MODE_OFF {
		return queryerContext.QueryContext(ctx, query, args)
	}
	var (
		err        error
		kerr       *keploy.KError = &keploy.KError{}
		driverRows *Rows          = &Rows{
			ctx:   ctx,
			query: query,
			args:  args,
			log:   c.log,
		}
		rows driver.Rows
	)
	kctx, er := internal.GetState(ctx)
	if er != nil {
		return nil, er
	}
	mode := kctx.Mode
	switch mode {
	case keploy.MODE_TEST:
		// don't run
	case keploy.MODE_RECORD:
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
		mockErr = ksqlErr.ConvertKError(mockErr)
		return driverRows, mockErr
	}
	return driverRows, err
}
