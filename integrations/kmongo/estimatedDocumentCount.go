package kmongo

import (
	"context"
	"fmt"

	"github.com/keploy/go-sdk/keploy"
	"go.keploy.io/server/pkg/models"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// EstimatedDocumentCount method mocks Collection.EstimatedDocumentCount of mongo.Collection. Actual method isn't called in test mode only as stated in integrations.NewCollection.
//
// See https://pkg.go.dev/go.mongodb.org/mongo-driver/mongo#Collection.EstimatedDocumentCount for information about Collection.EstimatedDocumentCount.
func (c *Collection) EstimatedDocumentCount(ctx context.Context,
			opts ...*options.EstimatedDocumentCountOptions) (int64, error){

	if keploy.GetModeFromContext(ctx) == keploy.MODE_OFF {
		output, err := c.Collection.EstimatedDocumentCount(ctx, opts...)
		return output, err
	}
	var (
		output int64
		err error
		kerr = &keploy.KError{}
		data []interface{}
	)

	for _, option := range opts {
		data = append(data, option)
	}
	o, e := c.getOutput(ctx, "EstimatedDocumentCount", data)
	if o != nil {
		output = o.(int64)
	}
	err = e

	derivedOpts := []options.EstimatedDocumentCountOptions{}
	for _, option := range opts {
		derivedOpts = append(derivedOpts, *option)
	}
	meta := map[string]string{
		"name":            									 "mongodb",
		"type":            									 string(models.NoSqlDB),
		"operation":       									 "EstimatedDocumentCount",
		"EstimatedDocumentCountOptions": 		 fmt.Sprint(derivedOpts),
	}

	if err != nil {
		kerr = &keploy.KError{Err: err}
		output = 0
	}
	mock, res := keploy.ProcessDep(ctx, c.log, meta, output, kerr) 

	if mock {
		var mockOutput int64
		var mockErr error
		if res[0] != nil {
			mockOutput = res[0].(int64)
		}
		x := res[1].(*keploy.KError)
		if x.Err != nil {
			mockErr = x.Err
		}
		return mockOutput, mockErr
	}
	return output, err
}
