package kmongo

import (
	"context"
	"errors"
	"fmt"

	"github.com/keploy/go-sdk/keploy"
	"go.keploy.io/server/pkg/models"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.uber.org/zap"
)

// SingleResult countains instance of mongo.SingleResult to mock its methods so that:
//  - In "capture" mode, stores the encoded output(generated by mocked methods of mongo.SingleResult) into keploy's Context Deps array.
//  - In "test" mode, decodes its stored encoded output which are present in the keploy's Context Deps array without calling mocked methods of mongo.SingleResult.
//  - In "off" mode, returns the output generated after calling mocked method of mongo.SingleResult.
type SingleResult struct {
	mongo.SingleResult
	filter interface{}
	opts   []options.FindOneOptions
	ctx    context.Context
	log    *zap.Logger
}

// Err mocks mongo's SingleResult Err() which will called in "capture" or "off" mode as stated above in SingleResult.
//
// See https://pkg.go.dev/go.mongodb.org/mongo-driver/mongo#SingleResult.Err for more information about SingleResult.Err.
func (msr *SingleResult) Err() error {
	if keploy.GetModeFromContext(msr.ctx) == keploy.MODE_OFF {
		err := msr.SingleResult.Err()
		return err
	}
	var err error
	var kerr *keploy.KError = &keploy.KError{}
	kctx, er := keploy.GetState(msr.ctx)
	if er != nil {
		return er
	}
	mode := kctx.Mode
	switch mode {
	case "test":
		//don't run mongo query as it is stored in context
	case "capture":
		err = msr.SingleResult.Err()
	default:
		return errors.New("integrations: Not in a valid sdk mode")
	}

	meta := map[string]string{
		"name":           "mongodb",
		"type":           string(models.NoSqlDB),
		"operation":      "FindOne.Err",
		"filter":         fmt.Sprint(msr.filter),
		"FindOneOptions": fmt.Sprint(msr.opts),
	}

	if err != nil {
		kerr = &keploy.KError{Err: err}
	}
	mock, res := keploy.ProcessDep(msr.ctx, msr.log, meta, kerr)

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

// Decode mocks mongo's SingleResult.Decode which will called in "capture" or "off" mode as stated above in SingleResult.
//
// See https://pkg.go.dev/go.mongodb.org/mongo-driver/mongo#SingleResult.Decode for more information about SingleResult.Decode.
func (msr *SingleResult) Decode(v interface{}) error {
	if keploy.GetModeFromContext(msr.ctx) == keploy.MODE_OFF {
		err := msr.SingleResult.Decode(v)
		return err
	}
	var err error
	var kerr = &keploy.KError{}
	kctx, er := keploy.GetState(msr.ctx)
	if er != nil {
		return er
	}
	mode := kctx.Mode
	switch mode {
	case "test":
		//dont run mongo query as it is stored in context
	case "capture":
		err = msr.SingleResult.Decode(v)
	default:
		return errors.New("integrations: Not in a valid sdk mode")
	}

	meta := map[string]string{
		"name":           "mongodb",
		"type":           string(models.NoSqlDB),
		"operation":      "FindOne.Decode",
		"filter":         fmt.Sprint(msr.filter),
		"FindOneOptions": fmt.Sprint(msr.opts),
	}

	if err != nil {
		kerr = &keploy.KError{Err: err}
	}
	mock, res := keploy.ProcessDep(msr.ctx, msr.log, meta, v, kerr)

	if mock {
		var mockErr error
		// rv := reflect.ValueOf(v)
		// rv.Elem().Set(reflect.ValueOf(res[0]).Elem())

		x := res[1].(*keploy.KError)
		if x.Err != nil {
			mockErr = x.Err
		}
		return mockErr
	}
	return err
}