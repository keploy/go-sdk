package kmongo

import (
	"context"
	"fmt"

	"github.com/keploy/go-sdk/keploy"
	"go.keploy.io/server/pkg/models"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// Watch method mocks Collection.Watch of mongo.Collection. Actual method isn't called in test mode only as stated in integrations.NewCollection.
//
// See https://pkg.go.dev/go.mongodb.org/mongo-driver/mongo#Collection.Watch for information about Collection.Watch.
func (c *Collection) Watch(ctx context.Context, pipeline interface{},
			opts ...*options.ChangeStreamOptions) (*mongo.ChangeStream, error){

	if keploy.GetModeFromContext(ctx) == keploy.MODE_OFF {
		output, err := c.Collection.Watch(ctx, pipeline, opts...)
		return output, err
	}
	var (
		output = &mongo.ChangeStream{}
		err error
		kerr = &keploy.KError{}
		data []interface{}
	)

	data = append(data, pipeline)
	for _, option := range opts {
		data = append(data, option)
	}
	o, e := c.getOutput(ctx, "Watch", data)  
	if o != nil {
		output = o.(*mongo.ChangeStream)
	}
	err = e

	derivedOpts := []options.ChangeStreamOptions{}
	for _, option := range opts {
		derivedOpts = append(derivedOpts, *option)
	}
	meta := map[string]string{
		"name":             "mongodb",
		"type":             string(models.NoSqlDB),
		"operation":        "Watch",
		"pipeline":         fmt.Sprint(pipeline),
		"WatchOptions":     fmt.Sprint(derivedOpts),
	}

	if err != nil {
		kerr = &keploy.KError{Err: err}
		output = &mongo.ChangeStream{}
	}
	mock, res := keploy.ProcessDep(ctx, c.log, meta, output, kerr)

	if mock {
		var mockOutput *mongo.ChangeStream
		var mockErr error
		if res[0] != nil {
			mockOutput = res[0].(*mongo.ChangeStream)
		}
		x := res[1].(*keploy.KError)
		if x.Err != nil {
			mockErr = x.Err
		}
		return mockOutput, mockErr
	}
	return output, err
}
