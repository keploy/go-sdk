package ksql

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/keploy/go-sdk/integrations/ksql/ksqlErr"
	"github.com/keploy/go-sdk/keploy"
	internal "github.com/keploy/go-sdk/pkg/keploy"
	proto "go.keploy.io/server/grpc/regression"
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

	columns []*proto.SqlCol
	rows    []string
	err     []string
}

// Columns mocks the output of Columns method of your SQL driver.
func (r *Rows) Columns() []string {
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
	meta := map[string]string{
		"name":      "SQL",
		"type":      string(models.SqlDB),
		"operation": "QueryContext.Columns",
	}
	mode := kctx.Mode
	switch mode {
	case internal.MODE_TEST:
		// don't run
		if r.columns != nil {
			for _, j := range r.columns {
				*output = append(*output, j.Name)
			}
			return *output
		}
	case internal.MODE_RECORD:
		if r.Rows != nil {
			o := r.Rows.Columns()
			output = &o
			res := []*proto.SqlCol{}
			for _, j := range *output {
				res = append(res, &proto.SqlCol{Name: j, Type: "<nil>"})
			}
			r.columns = append(r.columns, res...)
		}
		res := make([][]byte, 1)
		err := keploy.Encode(output, res, 0)
		if err != nil {
			r.log.Error("dependency capture failed: failed to encode object", zap.String("type", reflect.TypeOf(output).String()), zap.String("test id", kctx.TestID), zap.Error(err))
		}
		kctx.Deps = append(kctx.Deps, models.Dependency{
			Name: meta["name"],
			Type: models.DependencyType(meta["type"]),
			Data: res,
			Meta: meta,
		})
		return *output
	default:
		return nil
	}

	mock, _ := keploy.ProcessDep(r.ctx, r.log, meta, output)
	if mock {
		return *output
	}
	return *output
}

// Close mocks the output of Close method of your SQL driver.
func (r *Rows) Close() error {
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
	meta := map[string]string{
		"name":      "SQL",
		"type":      string(models.SqlDB),
		"operation": "QueryContext.Close",
	}
	mode := kctx.Mode
	switch mode {
	case internal.MODE_TEST:
		// don't run actual rows.Close
		errStr := "nil"
		if len(r.err) > 0 {
			errStr = r.err[0]
			r.err = r.err[1:]
			return ksqlErr.ConvertKError(errors.New(errStr))
		}
	case internal.MODE_RECORD:
		if r.Rows != nil {
			err = r.Rows.Close()
		}
		errStr := "nil"
		if err != nil {
			errStr = err.Error()
			kerr = &keploy.KError{Err: err}
		}
		CaptureSqlMocks(kctx, r.log, meta, string(models.TableType), sqlOutput{
			Table: &proto.Table{
				Cols: r.columns,
				Rows: r.rows,
			},
			Count: 0,
			Err:   append(r.err, errStr),
		}, kerr)
		return err
	default:
		return errors.New("integrations: Not in a valid sdk mode")
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

func strToValue(str string, typeOf string) (driver.Value, error) {
	var (
		res driver.Value
		err error
	)
	if str == "<nil>" {
		return nil, nil
	}
	switch typeOf {
	case "float64":
		res, err = strconv.ParseFloat(str, 64)
	case "int64":
		res, err = strconv.ParseInt(str, 10, 64)
	case "string":
		res = str
	case "bool":
		res, err = strconv.ParseBool(str)
	case "time.Time":
		var t = time.Time{}
		t.UnmarshalJSON([]byte(str))
		res = t

	case "[]uint8":
		var bb []byte
		for _, ps := range strings.Split(strings.Trim(str, "[]"), " ") {
			pi, _ := strconv.Atoi(ps)
			bb = append(bb, byte(pi))
		}
		res = bb
	default:
		// res = str

	}
	return res, err
}

func valueToStr(val driver.Value) string {
	str := ""
	switch reflect.ValueOf(val).Kind() {
	case reflect.Float64:
		str = strconv.FormatFloat(val.(float64), 'f', -1, 64)
	case reflect.Int64:
		str = strconv.FormatInt(val.(int64), 10)
	case reflect.String:
		str = val.(string)
	case reflect.Bool:
		str = strconv.FormatBool(val.(bool))
	default:
		if v, ok := val.(time.Time); ok {
			x, _ := json.Marshal(v)
			str += string(x)
		} else {
			str = fmt.Sprintf("%v", val)
		}
	}
	return str
}

// Next mocks the outputs of Next method of sql Driver.
func (r *Rows) Next(dest []driver.Value) error {
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
	meta := map[string]string{
		"name":      "SQL",
		"type":      string(models.SqlDB),
		"operation": "QueryContext.Next",
	}
	mode := kctx.Mode
	switch mode {
	case internal.MODE_TEST:
		// don't run
		if r.rows != nil && len(r.rows) > 0 {
			row := strings.Split(r.rows[0], "` | `")
			// if TODO match the length of dest and row items
			for i, j := range row {
				if i == 0 && len(j) > 0 {
					j = j[2:]
				}
				if i == len(dest)-1 && len(j) > 4 {
					j = j[:len(j)-5]
				}
				dest[i], err = strToValue(j, r.columns[i].Type)
				if err != nil {
					r.log.Error("failed to convert string to driver.value in Stmt.Next", zap.Error(err))
					return err
				}
			}
			r.rows = r.rows[1:]
			errStr := "nil"
			if len(r.err) > 0 {
				errStr = r.err[0]
				r.err = r.err[1:]
			}
			return ksqlErr.ConvertKError(errors.New(errStr))
		}
	case internal.MODE_RECORD:
		if r.Rows != nil {
			err = r.Rows.Next(dest)
			output.Value = dest
			str := "["
			for i, j := range dest {
				if r.columns[i].Type == "<nil>" {
					r.columns[i].Type = fmt.Sprintf("%T", j)
				}
				str += "`" + valueToStr(j) + "` | "
			}
			str += "]"
			r.columns = r.columns[0:len(dest)]
			r.rows = append(r.rows, str)
			str = "nil"
			if err != nil {
				str = err.Error()
			}
			r.err = append(r.err, str)
		}
		if err != nil {
			kerr = &keploy.KError{Err: err}
		}
		res := make([][]byte, 2)
		err2 := keploy.Encode(kerr, res, 0)
		if err2 != nil {
			r.log.Error("dependency capture failed: failed to encode object", zap.String("type", reflect.TypeOf(kerr).String()), zap.String("test id", kctx.TestID), zap.Error(err))
		}
		err2 = keploy.Encode(output, res, 1)
		if err2 != nil {
			r.log.Error("dependency capture failed: failed to encode object", zap.String("type", reflect.TypeOf(output).String()), zap.String("test id", kctx.TestID), zap.Error(err))
		}
		kctx.Deps = append(kctx.Deps, models.Dependency{
			Name: meta["name"],
			Type: models.DependencyType(meta["type"]),
			Data: res,
			Meta: meta,
		})
		return err
	default:
		return errors.New("integrations: Not in a valid sdk mode")
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
	meta := map[string]string{
		"name":      "SQL",
		"type":      string(models.SqlDB),
		"operation": "QueryContext",
		"query":     fmt.Sprintf(`"%v"`, query),
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
			meta1 := cloneMap(meta)
			meta1["operation"] = "QueryContext.Close"
			o1, ok1 := MockSqlFromYaml(kctx, meta1)
			if ok1 {
				driverRows.columns = o1.Table.Cols
				driverRows.rows = o1.Table.Rows
				driverRows.err = o1.Err
			}
			return driverRows, ksqlErr.ConvertKError(errors.New(o.Err[0]))
		}
	case internal.MODE_RECORD:
		rows, err = queryerContext.QueryContext(ctx, query, args)
		driverRows.Rows = rows
		errStr := "nil"
		if err != nil {
			kerr = &keploy.KError{Err: err}
			errStr = err.Error()
		}
		CaptureSqlMocks(kctx, c.log, meta, string(models.ErrType), sqlOutput{
			Count: 0,
			Err:   []string{errStr},
		}, kerr)
		// driverRows.err = append(driverRows.err, errStr)
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
