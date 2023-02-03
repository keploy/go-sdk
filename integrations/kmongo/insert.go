package kmongo

import (
	"context"
	"fmt"

	"github.com/keploy/go-sdk/keploy"
	internal "github.com/keploy/go-sdk/pkg/keploy"
	"go.keploy.io/server/pkg/models"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// InsertOne method mocks Collection.InsertOne of mongo.Collection. Actual method isn't called in test mode only as stated in integrations.NewCollection.
//
// See https://pkg.go.dev/go.mongodb.org/mongo-driver/mongo#Collection.InsertOne for information about Collection.InsertOne.
func (c *Collection) InsertOne(ctx context.Context, document interface{},
	opts ...*options.InsertOneOptions) (*mongo.InsertOneResult, error) {
	if internal.GetModeFromContext(ctx) == internal.MODE_OFF {
		output, err := c.Collection.InsertOne(ctx, document, opts...)
		return output, err
	}
	var (
		output = &mongo.InsertOneResult{}
		err    error
		kerr   = &keploy.KError{}
		data   []interface{}
	)
	data = append(data, document)
	for _, j := range opts {
		data = append(data, j)
	}
	o, e := c.getOutput(ctx, "InsertOne", data)
	if o != nil {
		output = o.(*mongo.InsertOneResult)
	}
	err = e

	derivedOpts := []options.InsertOneOptions{}
	for _, j := range opts {
		derivedOpts = append(derivedOpts, *j)
	}
	meta := map[string]string{
		"name":             "mongodb",
		"type":             string(models.NoSqlDB),
		"operation":        "InsertOne",
		"document":         fmt.Sprint(document),
		"InsertOneOptions": fmt.Sprint(derivedOpts),
	}

	if err != nil {
		kerr = &keploy.KError{Err: err}
		output = &mongo.InsertOneResult{}
	}
	mock, res := keploy.ProcessDep(ctx, c.log, meta, output, kerr)

	if mock {
		var mockOutput *mongo.InsertOneResult
		var mockErr error
		if res[0] != nil {
			mockOutput = res[0].(*mongo.InsertOneResult)
		}
		x := res[1].(*keploy.KError)
		if x.Err != nil {
			mockErr = x.Err
		}
		return mockOutput, mockErr
	}
	return output, err
}

// InsertMany method mocks Collection.InsertMany of mongo.
//
// For information about Collection.InsertMany, visit https://pkg.go.dev/go.mongodb.org/mongo-driver@v1.8.0/mongo#Collection.InsertMany.
func (c *Collection) InsertMany(ctx context.Context, documents []interface{},
	opts ...*options.InsertManyOptions) (*mongo.InsertManyResult, error) {
	if internal.GetModeFromContext(ctx) == internal.MODE_OFF {
		output, err := c.Collection.InsertMany(ctx, documents, opts...)
		return output, err
	}
	var (
		output = &mongo.InsertManyResult{}
		err    error
		kerr   *keploy.KError = &keploy.KError{}
		data   []interface{}
	)
	data = append(data, documents)
	for _, j := range opts {
		data = append(data, j)
	}
	o, e := c.getOutput(ctx, "InsertMany", data)
	if o != nil {
		output = o.(*mongo.InsertManyResult)
	}
	err = e

	derivedOpts := []options.InsertManyOptions{}
	for _, j := range opts {
		derivedOpts = append(derivedOpts, *j)
	}
	meta := map[string]string{
		"name":              "mongodb",
		"type":              string(models.NoSqlDB),
		"operation":         "InsertMany",
		"documents":         fmt.Sprint(documents),
		"InsertManyOptions": fmt.Sprint(derivedOpts),
	}

	if err != nil {
		kerr = &keploy.KError{Err: err}
		output = &mongo.InsertManyResult{}
	}
	mock, res := keploy.ProcessDep(ctx, c.log, meta, output, kerr)

	if mock {
		var mockOutput *mongo.InsertManyResult
		var mockErr error
		if res[0] != nil {
			mockOutput = res[0].(*mongo.InsertManyResult)
		}
		x := res[1].(*keploy.KError)
		if x.Err != nil {
			mockErr = x.Err
		}
		return mockOutput, mockErr
	}
	return output, err
}
