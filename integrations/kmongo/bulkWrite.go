package kmongo

import (
	"context"
	"fmt"

	"github.com/keploy/go-sdk/keploy"
	"go.keploy.io/server/pkg/models"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// BulkWrite method mocks Collection.BulkWrite of mongo.Collection. Actual method isn't called in test mode only as stated in integrations.NewCollection.
//
// See https://pkg.go.dev/go.mongodb.org/mongo-driver/mongo#Collection.BulkWrite for information about Collection.BulkWrite.
func (c *Collection) BulkWrite(ctx context.Context, models []mongo.WriteModel,
			opts ...*options.BulkWriteOptions) (*mongo.BulkWriteResult, error) {

	if keploy.GetModeFromContext(ctx) == keploy.MODE_OFF {
		output, err := c.Collection.BulkWrite(ctx, models, opts...)
		return output, err
	}
	var (
		output = &mongo.BulkWriteResult{}
		err error
		kerr = &keploy.KError{}
		data []interface{}
	)

	data = append(data, models)
	for _, option := range opts {
		data = append(data, option)
	}
	o, e := c.getOutput(ctx, "BulkWrite", data)
	if o != nil {
		output = o.(*mongo.BulkWriteResult)
	}
	err = e

	derivedOpts := []options.BulkWriteOptions{}
	for _, option := range opts {
		derivedOpts = append(derivedOpts, *option)
	}
	meta := map[string]string{
		"name":             "mongodb",
		"type":             string(models.NoSqlDB),
		"operation":        "BulkWrite",
		"models":           fmt.Sprint(models),
		"BulkWriteOptions": fmt.Sprint(derivedOpts),
	}

	if err != nil {
		kerr = &keploy.KError{Err: err}
		output = &mongo.BulkWriteResult{}
	}
	mock, res := keploy.ProcessDep(ctx, c.log, meta, output, kerr)

	if mock {
		var mockOutput *mongo.BulkWriteResult
		var mockErr error
		if res[0] != nil {
			mockOutput = res[0].(*mongo.BulkWriteResult)
		}
		x := res[1].(*keploy.KError)
		if x.Err != nil {
			mockErr = x.Err
		}
		return mockOutput, mockErr
	}
	return output, err
}
