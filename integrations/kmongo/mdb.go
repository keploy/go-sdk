package kmongo

import (
	"context"
	"encoding/gob"
	"errors"
	"fmt"

	"go.keploy.io/server/pkg/models"

	"github.com/keploy/go-sdk/keploy"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.uber.org/zap"
)

// NewCollection creates and returns an instance of Collection that contains actual
// pointer to mongo's collection. This is done in order to mock mongo CRUD operations, so that:
//  - In "record" mode, stores the encoded output(generated by mocked methods of mongo.Collection) into keploy's Context Deps array.
//  - In "test" mode, decodes its stored encoded output which are present in the keploy's Context Deps array without calling mocked methods of mongo.Collection.
//  - In "off" mode, returns the output generated after calling mocked method of mongo.Collection.
//
// cl parameter is pointer to mongo's collection instance created by (*mongo.Database).Collection
// method. It should not be nil, else warning will logged and nil is returned.
//
// Returns pointer to integrations.Collection which contains mongo.Collection. Nil is returned when mongo.Collection is nil.
func NewCollection(cl *mongo.Collection) *Collection {
	if cl == nil {
		return nil
	}
	gob.Register(primitive.ObjectID{})
	logger, _ := zap.NewProduction()
	defer func() {
		_ = logger.Sync() // flushes buffer, if any
	}()
	return &Collection{Collection: *cl, log: logger}
}

// Collection In order to mock mongo operations, mongo collection is embedded into Collection.
type Collection struct {
	mongo.Collection
	log *zap.Logger
}

// Distinct method mocks Collection.Distinct of mongo inorder to call it only in "record" or "off" mode.
//
// See https://pkg.go.dev/go.mongodb.org/mongo-driver/mongo#Collection.Distinct for more info about Distinct.
func (c *Collection) Distinct(ctx context.Context, fieldName string, filter interface{}, opts ...*options.DistinctOptions) ([]interface{}, error) {
	if keploy.GetModeFromContext(ctx) == keploy.MODE_OFF {
		output, err := c.Collection.Distinct(ctx, fieldName, filter, opts...)
		return output, err
	}
	var (
		output *[]interface{} = &[]interface{}{}
		err    error
		kerr   = &keploy.KError{}
		data   []interface{}
	)
	data = append(data, fieldName)
	data = append(data, filter)
	for _, j := range opts {
		data = append(data, j)
	}
	o, e := c.getOutput(ctx, "Distinct", data)
	if o != nil {
		dis := o.([]interface{})
		output = &dis
	}
	err = e
	derivedOpts := []options.DistinctOptions{}
	for _, j := range opts {
		derivedOpts = append(derivedOpts, *j)
	}
	meta := map[string]string{
		"name":            "mongodb",
		"type":            string(models.NoSqlDB),
		"operation":       "Distinct",
		"fieldName":       fieldName,
		"filter":          fmt.Sprint(filter),
		"DistinctOptions": fmt.Sprint(derivedOpts),
	}
	kerr.Err = err
	mock, res := keploy.ProcessDep(ctx, c.log, meta, output, kerr)
	if mock {
		var mockErr error
		x := res[1].(*keploy.KError)
		if x.Err != nil {
			mockErr = x.Err
		}
		return *output, mockErr
	}
	return *output, err
}

// CountDocuments method mocks Collection.CountDocuments of mongo inorder to call it only in "record" or "off" mode.
//
// See https://pkg.go.dev/go.mongodb.org/mongo-driver/mongo#Collection.CountDocuments for more info about CountDocuments.
func (c *Collection) CountDocuments(ctx context.Context, filter interface{}, opts ...*options.CountOptions) (int64, error) {
	if keploy.GetModeFromContext(ctx) == keploy.MODE_OFF {
		output, err := c.Collection.CountDocuments(ctx, filter, opts...)
		return output, err
	}
	var (
		output *int64
		err    error
		kerr   *keploy.KError = &keploy.KError{}
		data   []interface{}
	)
	data = append(data, filter)
	for _, j := range opts {
		data = append(data, j)
	}
	o, e := c.getOutput(ctx, "CountDocuments", data)
	if o != nil {
		count := o.(int64)
		output = &count
	}
	err = e
	derivedOpts := []options.CountOptions{}
	for _, j := range opts {
		derivedOpts = append(derivedOpts, *j)
	}
	meta := map[string]string{
		"name":         "mongodb",
		"type":         string(models.NoSqlDB),
		"operation":    "CountDocuments",
		"filter":       fmt.Sprint(filter),
		"CountOptions": fmt.Sprint(derivedOpts),
	}
	kerr.Err = err
	if output == nil {
		var count int64 = 0
		output = &count
	}
	mock, res := keploy.ProcessDep(ctx, c.log, meta, output, kerr)
	if mock {
		var mockErr error
		x := res[1].(*keploy.KError)
		if x.Err != nil {
			mockErr = x.Err
		}
		return *output, mockErr
	}
	return *output, err
}

// Aggregate method mocks Collection.Aggregate of mongo inorder to call it only in "record" or "off" mode.
//
// See https://pkg.go.dev/go.mongodb.org/mongo-driver/mongo#Collection.Aggregate for more info about Aggregate.
func (c *Collection) Aggregate(ctx context.Context, pipeline interface{}, opts ...*options.AggregateOptions) (*Cursor, error) {
	if keploy.GetModeFromContext(ctx) == keploy.MODE_OFF {
		cursor, err := c.Collection.Aggregate(ctx, pipeline, opts...)
		return &Cursor{
			Cursor:   *cursor,
			pipeline: pipeline,
			ctx:      ctx,
			log:      c.log,
		}, err
	}

	derivedOpts := []options.AggregateOptions{}
	for _, j := range opts {
		derivedOpts = append(derivedOpts, *j)
	}
	kctx, er := keploy.GetState(ctx)
	if er != nil {
		return &Cursor{
			pipeline:      pipeline,
			aggregateOpts: derivedOpts,
			log:           c.log,
			ctx:           ctx,
		}, er
	}
	mode := kctx.Mode
	var (
		cursor *mongo.Cursor
		err    error
	)
	switch mode {
	case "test":
		//don't call method in test mode
		return &Cursor{
			pipeline:      pipeline,
			aggregateOpts: derivedOpts,
			log:           c.log,
			ctx:           ctx,
		}, err
	case "record":
		cursor, err = c.Collection.Aggregate(ctx, pipeline, opts...)
		return &Cursor{
			Cursor:        *cursor,
			pipeline:      pipeline,
			aggregateOpts: derivedOpts,
			log:           c.log,
			ctx:           ctx,
		}, err
	default:
		c.log.Error("integrations: Not in a valid sdk mode")
		return &Cursor{
			pipeline:      pipeline,
			aggregateOpts: derivedOpts,
			log:           c.log,
			ctx:           ctx,
		}, errors.New("integrations: Not in a valid sdk mode")
	}

}

func (c *Collection) getOutput(ctx context.Context, str string, data []interface{}) (interface{}, error) {
	var (
		output interface{}
		err    error
	)
	kctx, er := keploy.GetState(ctx)
	if er != nil {
		return nil, er
	}
	mode := kctx.Mode
	switch mode {
	case "test":
		//dont run mongo query as it is stored in context
		err = nil
	case "record":
		output, err = c.callMethod(ctx, str, data)

	default:
		return nil, errors.New("integrations: Not in a valid sdk mode")
	}
	return output, err
}

func (c *Collection) callMethod(ctx context.Context, str string, data []interface{}) (interface{}, error) {
	var (
		output interface{}
		err    error
	)
	switch str {
	case "InsertOne":
		doc := data[0]
		data = data[1:]
		var opts []*options.InsertOneOptions
		for _, d := range data {
			opts = append(opts, d.(*options.InsertOneOptions))
		}
		output, err = c.Collection.InsertOne(ctx, doc, opts...)
	case "InsertMany":
		doc := data[0].([]interface{})
		data = data[1:]
		var opts []*options.InsertManyOptions
		for _, d := range data {
			opts = append(opts, d.(*options.InsertManyOptions))
		}
		output, err = c.Collection.InsertMany(ctx, doc, opts...)
	case "UpdateOne":
		filter := data[0]
		update := data[1]
		data = data[2:]
		var opts []*options.UpdateOptions
		for _, d := range data {
			opts = append(opts, d.(*options.UpdateOptions))
		}
		output, err = c.Collection.UpdateOne(ctx, filter, update, opts...)
	case "UpdateMany":
		filter := data[0]
		update := data[1]
		data = data[2:]
		var opts []*options.UpdateOptions
		for _, d := range data {
			opts = append(opts, d.(*options.UpdateOptions))
		}
		output, err = c.Collection.UpdateMany(ctx, filter, update, opts...)
	case "DeleteOne":
		filter := data[0]
		data = data[1:]
		var opts []*options.DeleteOptions
		for _, d := range data {
			opts = append(opts, d.(*options.DeleteOptions))
		}
		output, err = c.Collection.DeleteOne(ctx, filter, opts...)
	case "DeleteMany":
		filter := data[0]
		data = data[1:]
		var opts []*options.DeleteOptions
		for _, d := range data {
			opts = append(opts, d.(*options.DeleteOptions))
		}
		output, err = c.Collection.DeleteMany(ctx, filter, opts...)
	case "Distinct":
		fieldName := data[0]
		filter := data[1]
		data = data[2:]
		var opts []*options.DistinctOptions
		for _, d := range data {
			opts = append(opts, d.(*options.DistinctOptions))
		}
		output, err = c.Collection.Distinct(ctx, fieldName.(string), filter, opts...)
	case "CountDocuments":
		filter := data[0]
		data = data[1:]
		var opts []*options.CountOptions
		for _, d := range data {
			opts = append(opts, d.(*options.CountOptions))
		}
		output, err = c.Collection.CountDocuments(ctx, filter, opts...)
	default:
		return nil, errors.New("integerations: SDK Not supported for this method")
	}
	return output, err
}
