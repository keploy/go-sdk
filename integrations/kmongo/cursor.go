package kmongo

import (
	"context"
	"errors"
	"fmt"

	"github.com/keploy/go-sdk/keploy"
	internal "github.com/keploy/go-sdk/pkg/keploy"
	"go.keploy.io/server/pkg/models"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.uber.org/zap"
)

// Cursor contains emedded mongo.Cursor in order to override its methods.
type Cursor struct {
	mongo.Cursor
	filter        interface{}
	pipeline      interface{}
	findOpts      []options.FindOptions
	aggregateOpts []options.AggregateOptions
	ctx           context.Context
	log           *zap.Logger
}

// Err mocks mongo's Cursor.Err in order to store and replay its output according SDK mode.
//
// See https://pkg.go.dev/go.mongodb.org/mongo-driver/mongo#Cursor.Err for information about Cursor.Err.
func (cr *Cursor) Err() error {
	if internal.GetModeFromContext(cr.ctx) == internal.MODE_OFF {
		err := cr.Cursor.Err()
		return err
	}
	var err error
	var kerr = &keploy.KError{}
	kctx, er := internal.GetState(cr.ctx)
	if er != nil {
		return er
	}
	mode := kctx.Mode
	switch mode {
	case internal.MODE_TEST:
		//dont run mongo query as it is stored in context
		err = nil
	case internal.MODE_RECORD:
		err = cr.Cursor.Err()
	default:
		return errors.New("integrations: Not in a valid sdk mode")
	}

	meta := map[string]string{
		"name": "mongodb",
		"type": string(models.NoSqlDB),
	}
	if cr.filter != nil {
		meta["filter"] = fmt.Sprint(cr.filter)
		meta["FindOptions"] = fmt.Sprint(cr.findOpts)
		meta["operation"] = "Find.Err"
	} else {
		meta["pipeline"] = fmt.Sprint(cr.pipeline)
		meta["AggregateOptions"] = fmt.Sprint(cr.aggregateOpts)
		meta["operation"] = "Aggregate.Err"

	}

	if err != nil {
		kerr = &keploy.KError{Err: err}
	}
	mock, res := keploy.ProcessDep(cr.ctx, cr.log, meta, kerr)

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

// Close mocks mongo's Cursor.Close in order to store and replay its output according SDK mode.
//
// See https://pkg.go.dev/go.mongodb.org/mongo-driver/mongo#Cursor.Close for information about Cursor.Close.
func (cr *Cursor) Close(ctx context.Context) error {
	if internal.GetModeFromContext(ctx) == internal.MODE_OFF {
		err := cr.Cursor.Close(ctx)
		return err
	}
	var err error
	var kerr = &keploy.KError{}
	kctx, er := internal.GetState(cr.ctx)
	if er != nil {
		return er
	}
	mode := kctx.Mode
	switch mode {
	case internal.MODE_TEST:
		//dont run mongo query as it is stored in context
		err = nil
	case internal.MODE_RECORD:
		err = cr.Cursor.Close(ctx)
	default:
		return errors.New("integrations: Not in a valid sdk mode")
	}

	meta := map[string]string{
		"name": "mongodb",
		"type": string(models.NoSqlDB),
	}
	if cr.filter != nil {
		meta["filter"] = fmt.Sprint(cr.filter)
		meta["FindOptions"] = fmt.Sprint(cr.findOpts)
		meta["operation"] = "Find.Close"
	} else {
		meta["pipeline"] = fmt.Sprint(cr.pipeline)
		meta["AggregateOptions"] = fmt.Sprint(cr.aggregateOpts)
		meta["operation"] = "Aggregate.Close"

	}

	if err != nil {
		kerr = &keploy.KError{Err: err}
	}
	mock, res := keploy.ProcessDep(cr.ctx, cr.log, meta, kerr)

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

// TryNext mocks mongo's Cursor.TryNext in order to store and replay its output according SDK mode.
//
// See https://pkg.go.dev/go.mongodb.org/mongo-driver/mongo#Cursor.TryNext for information about Cursor.TryNext.
func (cr *Cursor) TryNext(ctx context.Context) bool {
	if internal.GetModeFromContext(ctx) == internal.MODE_OFF {
		return cr.Cursor.TryNext(ctx)
	}
	kctx, er := internal.GetState(cr.ctx)
	if er != nil {
		return false
	}
	var output *bool
	mode := kctx.Mode
	switch mode {
	case internal.MODE_TEST:
		//dont run mongo query as it is stored in context
		n := false
		output = &n
	case internal.MODE_RECORD:
		n := cr.Cursor.TryNext(ctx)
		output = &n
	default:
		return false
	}

	meta := map[string]string{
		"name": "mongodb",
		"type": string(models.NoSqlDB),
	}
	if cr.filter != nil {
		meta["filter"] = fmt.Sprint(cr.filter)
		meta["FindOptions"] = fmt.Sprint(cr.findOpts)
		meta["operation"] = "Find.TryNext"
	} else {
		meta["pipeline"] = fmt.Sprint(cr.pipeline)
		meta["AggregateOptions"] = fmt.Sprint(cr.aggregateOpts)
		meta["operation"] = "Aggregate.TryNext"

	}

	mock, res := keploy.ProcessDep(cr.ctx, cr.log, meta, output)

	if mock {
		if res[0] != nil {
			output = res[0].(*bool)
		}
	}
	return *output
}

// All mocks mongo's Cursor.All in order to store and replay its output according SDK mode.
//
// See https://pkg.go.dev/go.mongodb.org/mongo-driver/mongo#Cursor.All for information about Cursor.All.
func (cr *Cursor) All(ctx context.Context, results interface{}) error {
	if internal.GetModeFromContext(ctx) == internal.MODE_OFF {
		err := cr.Cursor.All(ctx, results)
		return err
	}
	var err error
	var kerr = &keploy.KError{}
	kctx, er := internal.GetState(cr.ctx)
	if er != nil {
		return er
	}
	mode := kctx.Mode
	switch mode {
	case internal.MODE_TEST:
		//dont run mongo query as it is stored in context
		err = nil
	case internal.MODE_RECORD:
		err = cr.Cursor.All(ctx, results)
	default:
		return errors.New("integrations: Not in a valid sdk mode")
	}

	meta := map[string]string{
		"name": "mongodb",
		"type": string(models.NoSqlDB),
	}
	if cr.filter != nil {
		meta["filter"] = fmt.Sprint(cr.filter)
		meta["FindOptions"] = fmt.Sprint(cr.findOpts)
		meta["operation"] = "Find.All"
	} else {
		meta["pipeline"] = fmt.Sprint(cr.pipeline)
		meta["AggregateOptions"] = fmt.Sprint(cr.aggregateOpts)
		meta["operation"] = "Aggregate.All"

	}

	if err != nil {
		kerr = &keploy.KError{Err: err}
	}
	mock, res := keploy.ProcessDep(cr.ctx, cr.log, meta, results, kerr)

	if mock {
		var mockErr error
		x := res[1].(*keploy.KError)
		if x.Err != nil {
			mockErr = x.Err
		}
		return mockErr
	}
	return err
}

// Next mocks mongo's Cursor.Next in order to store and replay its output according SDK mode.
//
// See https://pkg.go.dev/go.mongodb.org/mongo-driver/mongo#Cursor.Next for information about Cursor.Next.
func (cr *Cursor) Next(ctx context.Context) bool {
	if internal.GetModeFromContext(ctx) == internal.MODE_OFF {
		return cr.Cursor.Next(ctx)
	}
	kctx, er := internal.GetState(cr.ctx)
	if er != nil {
		return false
	}
	var output *bool
	mode := kctx.Mode
	switch mode {
	case internal.MODE_TEST:
		//dont run mongo query as it is stored in context
		n := false
		output = &n
	case internal.MODE_RECORD:
		n := cr.Cursor.Next(ctx)
		output = &n
	default:
		return false
	}

	meta := map[string]string{
		"name": "mongodb",
		"type": string(models.NoSqlDB),
	}
	if cr.filter != nil {
		meta["filter"] = fmt.Sprint(cr.filter)
		meta["FindOptions"] = fmt.Sprint(cr.findOpts)
		meta["operation"] = "Find.Next"
	} else {
		meta["pipeline"] = fmt.Sprint(cr.pipeline)
		meta["AggregateOptions"] = fmt.Sprint(cr.aggregateOpts)
		meta["operation"] = "Aggregate.Next"

	}

	mock, res := keploy.ProcessDep(cr.ctx, cr.log, meta, output)

	if mock {
		if res[0] != nil {
			output = res[0].(*bool)
		}
	}
	return *output
}

// Decode mocks mongo's Cursor.Decode in order to store and replay its output according SDK mode.
//
// See https://pkg.go.dev/go.mongodb.org/mongo-driver/mongo#Cursor.Decode for information about Cursor.Decode.
func (cr *Cursor) Decode(v interface{}) error {
	if internal.GetModeFromContext(cr.ctx) == internal.MODE_OFF {
		err := cr.Cursor.Decode(v)
		return err
	}
	var err error
	var kerr = &keploy.KError{}
	kctx, er := internal.GetState(cr.ctx)
	if er != nil {
		return er
	}
	mode := kctx.Mode
	switch mode {
	case internal.MODE_TEST:
		//dont run mongo query as it is stored in context
		err = nil
	case internal.MODE_RECORD:
		err = cr.Cursor.Decode(v)
	default:
		return errors.New("integrations: Not in a valid sdk mode")
	}

	meta := map[string]string{
		"name": "mongodb",
		"type": string(models.NoSqlDB),
	}
	if cr.filter != nil {
		meta["filter"] = fmt.Sprint(cr.filter)
		meta["FindOptions"] = fmt.Sprint(cr.findOpts)
		meta["operation"] = "Find.Decode"
	} else {
		meta["pipeline"] = fmt.Sprint(cr.pipeline)
		meta["AggregateOptions"] = fmt.Sprint(cr.aggregateOpts)
		meta["operation"] = "Aggregate.Decode"

	}

	if err != nil {
		kerr = &keploy.KError{Err: err}
	}
	mock, res := keploy.ProcessDep(cr.ctx, cr.log, meta, v, kerr)

	if mock {
		var mockErr error
		x := res[1].(*keploy.KError)
		if x.Err != nil {
			mockErr = x.Err
		}
		return mockErr
	}
	return err
}

// // Find creates and returns the instance of pointer to Cursor which have overridden methods of mongo.Cursor.
// // Actual Collection.Find is called only in keploy.MODE_RECORD or "off" mode.
// //
// // For information about Collection.Find, See https://pkg.go.dev/go.mongodb.org/mongo-driver/mongo#Collection.Find.
// func (c *Collection) Find(ctx context.Context, filter interface{},
// 	opts ...*options.FindOptions) (*Cursor, error) {
// 	if keploy.GetModeFromContext(ctx) == keploy.MODE_OFF {
// 		cursor, err := c.Collection.Find(ctx, filter, opts...)
// 		return &Cursor{
// 			Cursor: *cursor,
// 			filter: filter,
// 			ctx:    ctx,
// 			log:    c.log,
// 		}, err
// 	}

// 	derivedOpts := []options.FindOptions{}
// 	for _, j := range opts {
// 		derivedOpts = append(derivedOpts, *j)
// 	}
// 	kctx, er := keploy.GetState(ctx)
// 	if er != nil {
// 		return &Cursor{
// 			filter: filter,
// 			findOpts:   derivedOpts,
// 			log:    c.log,
// 			ctx:    ctx,
// 		}, er
// 	}
// 	mode := kctx.Mode
// 	var (
// 		cursor *mongo.Cursor
// 		err    error
// 	)
// 	switch mode {
// 	case keploy.MODE_TEST:
// 		//don't call method in test mode
// 		return &Cursor{
// 			filter: filter,
// 			findOpts:   derivedOpts,
// 			log:    c.log,
// 			ctx:    ctx,
// 		}, err
// 	case keploy.MODE_RECORD:
// 		cursor, err = c.Collection.Find(ctx, filter, opts...)
// 		return &Cursor{
// 			Cursor: *cursor,
// 			filter: filter,
// 			findOpts:   derivedOpts,
// 			log:    c.log,
// 			ctx:    ctx,
// 		}, err
// 	default:
// 		c.log.Error("integrations: Not in a valid sdk mode")
// 		return &Cursor{
// 			filter: filter,
// 			findOpts:   derivedOpts,
// 			log:    c.log,
// 			ctx:    ctx,
// 		}, errors.New("integrations: Not in a valid sdk mode")
// 	}

// }
