package integrations

import (
	"context"
	"encoding/gob"
	// "fmt"

	"github.com/keploy/go-agent/keploy"
	"go.uber.org/zap"

	// "go.mongodb.org/mongo-driver/bson"
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

type MongoSingleResult struct{
	Err error
	mongo.SingleResult
	Kcontext *keploy.Context
	ctx context.Context
	log *zap.Logger
}

func (msr *MongoSingleResult) Decode(v interface{}) error{

	var err error
	mode := keploy.GetMode()
	switch mode {
	case "test":
		//dont run mongo query as it is stored in context
		err = nil
	case "off":
		err = msr.SingleResult.Decode(v)
		return err
	default:
		err = msr.SingleResult.Decode(v)
	}

	meta := map[string]string{
		"operation": "Decode",
	}

	mock, res := keploy.ProcessDep(msr.ctx, msr.log, meta, v, err)

	if mock {
		var mockErr error
		if res[0] != nil {
			v = res[0]
		}
		if res[1] != nil {
			mockErr = res[1].(error)
		}
		return  mockErr
	}
	return err
}

func (c *MongoDB) FindOne(ctx context.Context, filter interface{}, opts ...*options.FindOneOptions) *MongoSingleResult {
	sr := c.Collection.FindOne(ctx, filter, opts...)
	kcontext,ok := ctx.Value(keploy.KCTX).(*keploy.Context)
	if !ok{
		c.log.Error("keploy context not present ")
	}
	return &MongoSingleResult{
		Err: sr.Err(),
		SingleResult: *sr,
		log: c.log,
		Kcontext: kcontext,
		ctx: ctx,
	}
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

type MongoCursor struct{
	Err error
	mongo.Cursor
	Kcontext *keploy.Context
	ctx context.Context
	log *zap.Logger
}

func (cr *MongoCursor) Decode(v interface{}) error{
	var err error
	mode := keploy.GetMode()
	switch mode {
	case "test":
		//dont run mongo query as it is stored in context
		err = nil
	case "off":
		err = cr.Cursor.Decode(v)
		return err
	default:
		err = cr.Cursor.Decode(v)
	}

	meta := map[string]string{
		"operation": "Decode",
	}

	mock, res := keploy.ProcessDep(cr.ctx, cr.log, meta, v, err)

	if mock {
		var mockErr error
		if res[0] != nil {
			v = res[0]
		}
		if res[1] != nil {
			mockErr = res[1].(error)
		}
		return  mockErr
	}
	return err
}

func (c *MongoDB) Find(ctx context.Context, filter interface{},
	opts ...*options.FindOptions) (*MongoCursor, error) {
	cursor,err := c.Collection.Find(ctx, filter, opts...)
	kcontext,ok := ctx.Value(keploy.KCTX).(*keploy.Context)
	if !ok{
		c.log.Error("keploy context not present ")
	}
	return &MongoCursor{
		Err: err,
		Cursor: *cursor,
		log: c.log,
		Kcontext: kcontext,
		ctx: ctx,
	}, err
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
