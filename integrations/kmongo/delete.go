package kmongo

import (
	"context"
	"fmt"

	"github.com/keploy/go-sdk/keploy"
	"go.keploy.io/server/pkg/models"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// DeleteOne method mocks Collection.DeleteOne of mongo.
//
// See https://pkg.go.dev/go.mongodb.org/mongo-driver@v1.8.0/mongo#Collection.DeleteOne for
// information about Collection.DeleteOne.
func (c *Collection) DeleteOne(ctx context.Context, filter interface{},
	opts ...*options.DeleteOptions) (*mongo.DeleteResult, error) {
	if keploy.GetModeFromContext(ctx) == keploy.MODE_OFF {
		output, err := c.Collection.DeleteOne(ctx, filter, opts...)
		return output, err
	}

	var (
		output = &mongo.DeleteResult{}
		err error
		kerr = &keploy.KError{}
		data []interface{}
	)
	data = append(data, filter)
	for _, j := range opts {
		data = append(data, j)
	}
	o, e := c.getOutput(ctx, "DeleteOne", data)
	if o != nil {
		output = o.(*mongo.DeleteResult)
	}
	err = e

	derivedOpts := []options.DeleteOptions{}
	for _, j := range opts {
		derivedOpts = append(derivedOpts, *j)
	}
	meta := map[string]string{
		"name":          "mongodb",
		"type":          string(models.NoSqlDB),
		"operation":     "DeleteOne",
		"filter":        fmt.Sprint(filter),
		"DeleteOptions": fmt.Sprint(derivedOpts),
	}

	if err != nil {
		kerr = &keploy.KError{Err: err}
		output = &mongo.DeleteResult{}
	}
	mock, res := keploy.ProcessDep(ctx, c.log, meta, output, kerr)

	if mock {
		var mockOutput *mongo.DeleteResult
		var mockErr error
		if res[0] != nil {
			mockOutput = res[0].(*mongo.DeleteResult)
		}
		x := res[1].(*keploy.KError)
		if x.Err != nil {
			mockErr = x.Err
		}
		return mockOutput, mockErr
	}
	return output, err
}

// DeleteMany method mocks Collection.DeleteMany of mongo inorder to call it only in "capture" or "off" mode.
//
// See https://pkg.go.dev/go.mongodb.org/mongo-driver@v1.8.0/mongo#Collection.DeleteMany for information about Collection.DeleteMany.
func (c *Collection) DeleteMany(ctx context.Context, filter interface{},
	opts ...*options.DeleteOptions) (*mongo.DeleteResult, error) {
	if keploy.GetModeFromContext(ctx) == keploy.MODE_OFF {
		output, err := c.Collection.DeleteMany(ctx, filter, opts...)
		return output, err
	}
	var (
		output *mongo.DeleteResult = &mongo.DeleteResult{}
		err error
		kerr = &keploy.KError{}
		data []interface{}
	)
	data = append(data, filter)
	for _, j := range opts {
		data = append(data, j)
	}
	o, e := c.getOutput(ctx, "DeleteMany", data)
	if o != nil {
		output = o.(*mongo.DeleteResult)
	}
	err = e

	derivedOpts := []options.DeleteOptions{}
	for _, j := range opts {
		derivedOpts = append(derivedOpts, *j)
	}
	meta := map[string]string{
		"name":          "mongodb",
		"type":          string(models.NoSqlDB),
		"operation":     "DeleteMany",
		"filter":        fmt.Sprint(filter),
		"DeleteOptions": fmt.Sprint(derivedOpts),
	}

	if err != nil {
		kerr = &keploy.KError{Err: err}
		output = &mongo.DeleteResult{}
	}
	mock, res := keploy.ProcessDep(ctx, c.log, meta, output, kerr)

	if mock {
		var mockOutput *mongo.DeleteResult
		var mockErr error
		if res[0] != nil {
			mockOutput = res[0].(*mongo.DeleteResult)
		}
		x := res[1].(*keploy.KError)
		if x.Err != nil {
			mockErr = x.Err
		}
		return mockOutput, mockErr
	}
	return output, err
}