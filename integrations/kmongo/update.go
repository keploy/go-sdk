package kmongo

import (
	"context"
	"fmt"

	"github.com/keploy/go-sdk/keploy"
	"go.keploy.io/server/pkg/models"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// UpdateOne method mocks Collection.UpdateOne of mongo.
//
// For information about Collection.UpdateOne, refer to https://pkg.go.dev/go.mongodb.org/mongo-driver@v1.8.0/mongo#Collection.UpdateOne.
func (c *Collection) UpdateOne(ctx context.Context, filter interface{}, update interface{},
	opts ...*options.UpdateOptions) (*mongo.UpdateResult, error) {
		
	if keploy.GetModeFromContext(ctx) == keploy.MODE_OFF {
		output, err := c.Collection.UpdateOne(ctx, filter, update, opts...)
		return output, err
	}
	var (
		output = &mongo.UpdateResult{}
		err error
		kerr = &keploy.KError{}
		data []interface{}
	)
	data = append(data, filter)
	data = append(data, update)
	for _, j := range opts {
		data = append(data, j)
	}
	o, e := c.getOutput(ctx, "UpdateOne", data)
	if o != nil {
		output = o.(*mongo.UpdateResult)
	}
	err = e

	derivedOpts := []options.UpdateOptions{}
	for _, j := range opts {
		derivedOpts = append(derivedOpts, *j)
	}
	meta := map[string]string{
		"name":          "mongodb",
		"type":          string(models.NoSqlDB),
		"operation":     "UpdateOne",
		"filter":        fmt.Sprint(filter),
		"update":        fmt.Sprint(update),
		"UpdateOptions": fmt.Sprint(derivedOpts),
	}

	if err != nil {
		kerr = &keploy.KError{Err: err}
		output = &mongo.UpdateResult{}
	}
	mock, res := keploy.ProcessDep(ctx, c.log, meta, output, kerr)

	if mock {
		var mockOutput *mongo.UpdateResult
		var mockErr error
		if res[0] != nil {
			mockOutput = res[0].(*mongo.UpdateResult)
		}
		x := res[1].(*keploy.KError)
		if x.Err != nil {
			mockErr = x.Err
		}
		return mockOutput, mockErr
	}
	return output, err
}

// UpdateMany method mocks Collection.UpdateMany of mongo.
//
// For information about Collection.UpdateMany, go to https://pkg.go.dev/go.mongodb.org/mongo-driver@v1.8.0/mongo#Collection.UpdateMany.
func (c *Collection) UpdateMany(ctx context.Context, filter interface{}, update interface{},
	opts ...*options.UpdateOptions) (*mongo.UpdateResult, error) {

	if keploy.GetModeFromContext(ctx) == keploy.MODE_OFF {
		output, err := c.Collection.UpdateMany(ctx, filter, update, opts...)
		return output, err
	}

	var (
		output = &mongo.UpdateResult{}
		err error
		kerr *keploy.KError = &keploy.KError{}
		data []interface{}
	)
	data = append(data, filter)
	data = append(data, update)
	for _, j := range opts {
		data = append(data, j)
	}

	o, e := c.getOutput(ctx, "UpdateMany", data)
	if o != nil {
		output = o.(*mongo.UpdateResult)
	}
	err = e

	derivedOpts := []options.UpdateOptions{}
	for _, j := range opts {
		derivedOpts = append(derivedOpts, *j)
	}
	meta := map[string]string{
		"name":          "mongodb",
		"type":          string(models.NoSqlDB),
		"operation":     "UpdateMany",
		"filter":        fmt.Sprint(filter),
		"update":        fmt.Sprint(update),
		"UpdateOptions": fmt.Sprint(derivedOpts),
	}

	if err != nil {
		kerr = &keploy.KError{Err: err}
		output = &mongo.UpdateResult{}
	}
	mock, res := keploy.ProcessDep(ctx, c.log, meta, output, kerr)

	if mock {
		var mockOutput *mongo.UpdateResult
		var mockErr error
		if res[0] != nil {
			mockOutput = res[0].(*mongo.UpdateResult)
		}
		x := res[1].(*keploy.KError)
		if x.Err != nil {
			mockErr = x.Err
		}
		return mockOutput, mockErr
	}
	return output, err
}
