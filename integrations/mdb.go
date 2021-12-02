package integrations

import (
	"context"
	"encoding/gob"

	"github.com/keploy/go-agent/keploy"
	"go.uber.org/zap"

	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func NewMongoDB(cl *mongo.Collection) *MongoDB {
	gob.Register(primitive.ObjectID{})
	logger, _ := zap.NewProduction()
	defer logger.Sync()
	return &MongoDB{Collection: *cl, log: logger}
}

type MongoDB struct {
	mongo.Collection
	log *zap.Logger
}

func (c *MongoDB) FindOne(ctx context.Context, filter interface{}, opts ...*options.FindOneOptions) *mongo.SingleResult {
	//We should do this only when GetMode is capture. But here it is calling for both.
	var output *mongo.SingleResult
	mode := keploy.GetMode()
	switch mode {
	case "test":
		//dont run mongo query as it is stored in context
		output = &mongo.SingleResult{}
	case "off":
		output = c.Collection.FindOne(ctx, filter, opts...)
		return output
	default:
		output = c.Collection.FindOne(ctx, filter, opts...)
	}

	meta := map[string]string{
		"operation": "FindOne",
	}

	mock, res := keploy.ProcessDep(ctx, c.log, meta, output)
	if mock {
		var mockOutput *mongo.SingleResult
		// var mockErr error
		if res[0] != nil {
			mockOutput = res[0].(*mongo.SingleResult)
		}
		// if res[1] != nil {
		// 	mockErr =  res[1].(error)
		// }
		return mockOutput
	}
	return output
}

func (c *MongoDB) InsertOne(ctx context.Context, document interface{},
	opts ...*options.InsertOneOptions) (*mongo.InsertOneResult, error) {

	var output *mongo.InsertOneResult
	var err error
	mode := keploy.GetMode()
	switch mode {
	case "test":
		//dont run mongo query as it is stored in context
		output = &mongo.InsertOneResult{}
		err = nil
	case "off":
		output, err = c.Collection.InsertOne(ctx, document, opts...)
		return output, err
	default:
		output, err = c.Collection.InsertOne(ctx, document, opts...)
	}

	if keploy.GetMode() == "off" {
		return output, err
	}
	meta := map[string]string{
		"operation": "InsertOne",
	}

	mock, res := keploy.ProcessDep(ctx, c.log, meta, output, err)

	if mock {
		var mockOutput *mongo.InsertOneResult
		var mockErr error
		if res[0] != nil {
			mockOutput = res[0].(*mongo.InsertOneResult)
		}
		if res[1] != nil {
			mockErr = res[1].(error)
		}
		return mockOutput, mockErr
	}
	return output, err
}

func (c *MongoDB) Find(ctx context.Context, filter interface{},
	opts ...*options.FindOptions) (*mongo.Cursor, error) {

	var output *mongo.Cursor
	var err error
	mode := keploy.GetMode()
	switch mode {
	case "test":
		//dont run mongo query as it is stored in context
		output = &mongo.Cursor{}
		err = nil
	case "off":
		output, err = c.Collection.Find(ctx, filter, opts...)
		return output, err
	default:
		output, err = c.Collection.Find(ctx, filter, opts...)
	}

	if keploy.GetMode() == "off" {
		return output, err
	}
	meta := map[string]string{
		"operation": "Find",
	}

	mock, res := keploy.ProcessDep(ctx, c.log, meta, output, err)

	if mock {
		var mockOutput *mongo.Cursor
		var mockErr error
		if res[0] != nil {
			mockOutput = res[0].(*mongo.Cursor)
		}
		if res[1] != nil {
			mockErr = res[1].(error)
		}
		return mockOutput, mockErr
	}
	return output, err
}

func (c *MongoDB) InsertMany(ctx context.Context, documents []interface{},
	opts ...*options.InsertManyOptions) (*mongo.InsertManyResult, error) {

	var output *mongo.InsertManyResult
	var err error
	mode := keploy.GetMode()
	switch mode {
	case "test":
		//dont run mongo query as it is stored in context
		output = &mongo.InsertManyResult{}
		err = nil
	case "off":
		output, err = c.Collection.InsertMany(ctx, documents, opts...)
		return output, err
	default:
		output, err = c.Collection.InsertMany(ctx, documents, opts...)
	}

	meta := map[string]string{
		"operation": "InsertMany",
	}

	mock, res := keploy.ProcessDep(ctx, c.log, meta, output, err)

	if mock {
		var mockOutput *mongo.InsertManyResult
		var mockErr error
		if res[0] != nil {
			mockOutput = res[0].(*mongo.InsertManyResult)
		}
		if res[1] != nil {
			mockErr = res[1].(error)
		}
		return mockOutput, mockErr
	}
	return output, err
}

func (c *MongoDB) UpdateOne(ctx context.Context, filter interface{}, update interface{},
	opts ...*options.UpdateOptions) (*mongo.UpdateResult, error) {

	var output *mongo.UpdateResult
	var err error
	mode := keploy.GetMode()
	switch mode {
	case "test":
		//dont run mongo query as it is stored in context
		output = &mongo.UpdateResult{}
		err = nil
	case "off":
		output, err = c.Collection.UpdateOne(ctx, filter, update, opts...)
		return output, err
	default:
		output, err = c.Collection.UpdateOne(ctx, filter, update, opts...)
	}

	if keploy.GetMode() == "off" {
		return output, err
	}
	meta := map[string]string{
		"operation": "UpdateOne",
	}

	mock, res := keploy.ProcessDep(ctx, c.log, meta, output, err)

	if mock {
		var mockOutput *mongo.UpdateResult
		var mockErr error
		if res[0] != nil {
			mockOutput = res[0].(*mongo.UpdateResult)
		}
		if res[1] != nil {
			mockErr = res[1].(error)
		}
		return mockOutput, mockErr
	}
	return output, err
}

func (c *MongoDB) UpdateMany(ctx context.Context, filter interface{}, update interface{},
	opts ...*options.UpdateOptions) (*mongo.UpdateResult, error) {

	var output *mongo.UpdateResult
	var err error
	mode := keploy.GetMode()
	switch mode {
	case "test":
		//dont run mongo query as it is stored in context
		output = &mongo.UpdateResult{}
		err = nil
	case "off":
		output, err = c.Collection.UpdateMany(ctx, filter, update, opts...)
		return output, err
	default:
		output, err = c.Collection.UpdateMany(ctx, filter, update, opts...)
	}

	if keploy.GetMode() == "off" {
		return output, err
	}
	meta := map[string]string{
		"operation": "UpdateMany",
	}

	mock, res := keploy.ProcessDep(ctx, c.log, meta, output, err)

	if mock {
		var mockOutput *mongo.UpdateResult
		var mockErr error
		if res[0] != nil {
			mockOutput = res[0].(*mongo.UpdateResult)
		}
		if res[1] != nil {
			mockErr = res[1].(error)
		}
		return mockOutput, mockErr
	}
	return output, err
}

func (c *MongoDB) DeleteOne(ctx context.Context, filter interface{},
	opts ...*options.DeleteOptions) (*mongo.DeleteResult, error) {

	var output *mongo.DeleteResult
	var err error
	mode := keploy.GetMode()
	switch mode {
	case "test":
		//dont run mongo query as it is stored in context
		output = &mongo.DeleteResult{}
		err = nil
	case "off":
		output, err = c.Collection.DeleteOne(ctx, filter, opts...)
		return output, err
	default:
		output, err = c.Collection.DeleteOne(ctx, filter, opts...)
	}

	if keploy.GetMode() == "off" {
		return output, err
	}
	meta := map[string]string{
		"operation": "DeleteOne",
	}

	mock, res := keploy.ProcessDep(ctx, c.log, meta, output, err)

	if mock {
		var mockOutput *mongo.DeleteResult
		var mockErr error
		if res[0] != nil {
			mockOutput = res[0].(*mongo.DeleteResult)
		}
		if res[1] != nil {
			mockErr = res[1].(error)
		}
		return mockOutput, mockErr
	}
	return output, err
}

func (c *MongoDB) DeleteMany(ctx context.Context, filter interface{},
	opts ...*options.DeleteOptions) (*mongo.DeleteResult, error) {

	var output *mongo.DeleteResult
	var err error
	mode := keploy.GetMode()
	switch mode {
	case "test":
		//dont run mongo query as it is stored in context
		output = &mongo.DeleteResult{}
		err = nil
	case "off":
		output, err = c.Collection.DeleteMany(ctx, filter, opts...)
		return output, err
	default:
		output, err = c.Collection.DeleteMany(ctx, filter, opts...)
	}

	if keploy.GetMode() == "off" {
		return output, err
	}
	meta := map[string]string{
		"operation": "DeleteMany",
	}

	mock, res := keploy.ProcessDep(ctx, c.log, meta, output, err)

	if mock {
		var mockOutput *mongo.DeleteResult
		var mockErr error
		if res[0] != nil {
			mockOutput = res[0].(*mongo.DeleteResult)
		}
		if res[1] != nil {
			mockErr = res[1].(error)
		}
		return mockOutput, mockErr
	}
	return output, err
}
