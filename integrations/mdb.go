package integrations

import (
	"context"
	"encoding/gob"
	"errors"

	// "fmt"

	// "errors"
	// "fmt"

	"github.com/keploy/go-sdk/keploy"
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

type MongoSingleResult struct {
	mongo.SingleResult
	ctx context.Context
	log *zap.Logger
}

func (msr *MongoSingleResult) Err() error {
	if keploy.GetMode() == "off" {
		err := msr.SingleResult.Err()
		return err
	}
	var err error
	var kerr *keploy.KError = &keploy.KError{Err: nil}
	kctx, er := keploy.GetState(msr.ctx)
	if er != nil {
		msr.log.Error(er.Error())
		return er
	}
	mode := kctx.Mode
	switch mode {
	case "test":
		//dont run mongo query as it is stored in context
	case "capture":
		err = msr.SingleResult.Err()
	default:
		msr.log.Error("integrations: Not in a valid sdk mode")
		return  errors.New("integrations: Not in a valid sdk mode")
	}

	meta := map[string]string{
		"name":      "mongodb",
		"type":      string(keploy.NoSqlDB),
		"operation": "FindOne.Err",
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

func (msr *MongoSingleResult) Decode(v interface{}) error {
	if keploy.GetMode() == "off" {
		err := msr.SingleResult.Decode(v)
		return err
	}
	var err error
	var kerr *keploy.KError = &keploy.KError{Err: nil}
	kctx, er := keploy.GetState(msr.ctx)
	if er != nil {
		msr.log.Error(er.Error())
		return er
	}
	mode := kctx.Mode
	switch mode {
	case "test":
		//dont run mongo query as it is stored in context
	case "capture":
		err = msr.SingleResult.Decode(v)
	default:
		msr.log.Error("integrations: Not in a valid sdk mode")
		return  errors.New("integrations: Not in a valid sdk mode")
	}

	meta := map[string]string{
		"name":      "mongodb",
		"type":      string(keploy.NoSqlDB),
		"operation": "FindOne.Decode",
	}

	if err != nil {
		kerr = &keploy.KError{Err: err}
	}
	mock, res := keploy.ProcessDep(msr.ctx, msr.log, meta, v, kerr)

	if mock {
		var mockErr error
		if res[0] != nil {
			v = res[0]
		}
		x := res[1].(*keploy.KError)
		if x.Err != nil {
			mockErr = x.Err
		}
		return mockErr
	}
	return err
}

func (c *MongoDB) FindOne(ctx context.Context, filter interface{}, opts ...*options.FindOneOptions) *MongoSingleResult {
	if keploy.GetMode() == "off" {
		sr := c.Collection.FindOne(ctx, filter, opts...)
		return &MongoSingleResult{
			SingleResult: *sr,
			log:          c.log,
			ctx:          ctx,
		}
	}
	kctx, er := keploy.GetState(ctx)
	if er != nil {
		c.log.Error(er.Error())
		return &MongoSingleResult{
			log: c.log,
			ctx: ctx,
		}
	}
	mode := kctx.Mode
	var sr *mongo.SingleResult
	switch mode {
	case "test":
		return &MongoSingleResult{
			log: c.log,
			ctx: ctx,
		}
	case "capture":
		sr = c.Collection.FindOne(ctx, filter, opts...)
	default:
		c.log.Error("integrations: Not in a valid sdk mode")
		return &MongoSingleResult{
			log: c.log,
			ctx: ctx,
		}
	}

	return &MongoSingleResult{
		SingleResult: *sr,
		log:          c.log,
		ctx:          ctx,
	}
}

func (c *MongoDB) InsertOne(ctx context.Context, document interface{},
	opts ...*options.InsertOneOptions) (*mongo.InsertOneResult, error) {
	if keploy.GetMode() == "off" {
		output, err := c.Collection.InsertOne(ctx, document, opts...)
		return output, err
	}
	var output *mongo.InsertOneResult
	var err error
	var kerr *keploy.KError = &keploy.KError{Err: nil}
	kctx, er := keploy.GetState(ctx)
	if er != nil {
		c.log.Error(er.Error())
		return nil,er
	}
	mode := kctx.Mode
	switch mode {
	case "test":
		//dont run mongo query as it is stored in context
		output = &mongo.InsertOneResult{}
	case "capture":
		output, err = c.Collection.InsertOne(ctx, document, opts...)
	default:
		c.log.Error("integrations: Not in a valid sdk mode")
		return nil,errors.New("integrations: Not in a valid sdk mode")
	}

	meta := map[string]string{
		"name":      "mongodb",
		"type":      string(keploy.NoSqlDB),
		"operation": "InsertOne",
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

type MongoCursor struct {
	mongo.Cursor
	ctx      context.Context
	log      *zap.Logger
}

func (cr *MongoCursor) Next(ctx context.Context) bool{
	if keploy.GetMode() == "off" {
		return cr.Cursor.Next(ctx)
	}
	kctx, er := keploy.GetState(cr.ctx)
	if er != nil {
		cr.log.Error(er.Error())
		return false
	}
	var output *bool 
	mode := kctx.Mode
	switch mode {
	case "test":
		//dont run mongo query as it is stored in context
		n := false
		output = &n
	case "capture":
		n := cr.Cursor.Next(ctx)
		output = &n
	default:
		cr.log.Error("integrations: Not in a valid SDK mode")
		return false
	}

	meta := map[string]string{
		"name":      "mongodb",
		"type":      string(keploy.NoSqlDB),
		"operation": "Find.Next",
	}
	
	mock, res := keploy.ProcessDep(cr.ctx, cr.log, meta, output)

	if mock {
		if res[0] != nil {
			output = res[0].(*bool)
		}
	}
	return *output
}

func (cr *MongoCursor) Decode(v interface{}) error {
	if keploy.GetMode() == "off" {
		err := cr.Cursor.Decode(v)
		return err
	}
	var err error
	var kerr *keploy.KError = &keploy.KError{Err: nil}
	kctx, er := keploy.GetState(cr.ctx)
	if er != nil {
		cr.log.Error(er.Error())
		return er
	}
	mode := kctx.Mode
	switch mode {
	case "test":
		//dont run mongo query as it is stored in context
		err = nil
	case "capture":
		err = cr.Cursor.Decode(v)
	default:
		cr.log.Error("integrations: Not in a valid sdk mode")
		return  errors.New("integrations: Not in a valid sdk mode")
	}

	meta := map[string]string{
		"name":      "mongodb",
		"type":      string(keploy.NoSqlDB),
		"operation": "Find.Decode",
	}

	if err != nil {
		kerr = &keploy.KError{Err: err}
	}
	mock, res := keploy.ProcessDep(cr.ctx, cr.log, meta, v, kerr)

	if mock {
		var mockErr error
		if res[0] != nil {
			v = res[0]
		}
		x := res[1].(*keploy.KError)
		if x.Err != nil {
			mockErr = x.Err
		}
		return mockErr
	}
	return err
}

//have to work on this. It might fail
func (c *MongoDB) Find(ctx context.Context, filter interface{},
	opts ...*options.FindOptions) (*MongoCursor, error) {
	if keploy.GetMode()=="off"{
		cursor, err := c.Collection.Find(ctx, filter, opts...)
		return &MongoCursor{
			Cursor: *cursor,
			ctx: ctx,
			log: c.log,
		}, err
	}
	
	kctx, er := keploy.GetState(ctx)
	if er != nil {
		c.log.Error(er.Error())
		return &MongoCursor{
			log: c.log,
			ctx: ctx,
		}, er
	}
	mode := kctx.Mode
	var (
		cursor *mongo.Cursor
		err error
	)
	switch mode {
	case "test":
		//don't call method in test mode	
		return &MongoCursor{
			log: c.log,
			ctx: ctx,
		}, err
	case "capture":
		cursor, err = c.Collection.Find(ctx, filter, opts...)
		return &MongoCursor{
			Cursor:   *cursor,
			log:      c.log,
			ctx:      ctx,
		}, err
	default:
		c.log.Error("integrations: Not in a valid sdk mode")
		return &MongoCursor{
			log: c.log,
			ctx: ctx,
		}, errors.New("integrations: Not in a valid sdk mode")
	}

}

func (c *MongoDB) InsertMany(ctx context.Context, documents []interface{},
	opts ...*options.InsertManyOptions) (*mongo.InsertManyResult, error) {
	if keploy.GetMode() == "off" {
		output, err := c.Collection.InsertMany(ctx, documents, opts...)
		return output, err
	}
	var output *mongo.InsertManyResult
	var err error
	var kerr *keploy.KError = &keploy.KError{Err: nil}
	kctx, er := keploy.GetState(ctx)
	if er != nil {
		c.log.Error(er.Error())
		return nil,er
	}
	mode := kctx.Mode
	switch mode {
	case "test":
		//dont run mongo query as it is stored in context
		output = &mongo.InsertManyResult{}
		err = nil
	case "capture":
		output, err = c.Collection.InsertMany(ctx, documents, opts...)
	default:
		c.log.Error("integrations: Not in a valid sdk mode")
		return nil, errors.New("integrations: Not in a valid sdk mode")
	}

	meta := map[string]string{
		"name":      "mongodb",
		"type":      string(keploy.NoSqlDB),
		"operation": "InsertMany",
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

func (c *MongoDB) UpdateOne(ctx context.Context, filter interface{}, update interface{},
	opts ...*options.UpdateOptions) (*mongo.UpdateResult, error) {
	if keploy.GetMode() == "off" {
		output, err := c.Collection.UpdateOne(ctx, filter, update, opts...)
		return output, err
	}
	var output *mongo.UpdateResult
	var err error
	var kerr *keploy.KError = &keploy.KError{Err: nil}
	kctx, er := keploy.GetState(ctx)
	if er != nil {
		c.log.Error(er.Error())
		return nil,er
	}
	mode := kctx.Mode
	switch mode {
	case "test":
		//dont run mongo query as it is stored in context
		output = &mongo.UpdateResult{}
		err = nil
	case "capture":
		output, err = c.Collection.UpdateOne(ctx, filter, update, opts...)
	default:
		c.log.Error("integrations: Not in a valid sdk mode")
		return nil, errors.New("integrations: Not in a valid sdk mode")
	}

	meta := map[string]string{
		"name":      "mongodb",
		"type":      string(keploy.NoSqlDB),
		"operation": "UpdateOne",
	}

	if err != nil {
		kerr = &keploy.KError{Err: err}
		c.log.Error(err.Error())
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

func (c *MongoDB) UpdateMany(ctx context.Context, filter interface{}, update interface{},
	opts ...*options.UpdateOptions) (*mongo.UpdateResult, error) {
	if keploy.GetMode() == "off" {
		output, err := c.Collection.UpdateMany(ctx, filter, update, opts...)
		return output, err
	}
	var output *mongo.UpdateResult
	var err error
	var kerr *keploy.KError = &keploy.KError{Err: nil}
	kctx, er := keploy.GetState(ctx)
	if er != nil {
		c.log.Error(er.Error())
		return nil,er
	}
	mode := kctx.Mode
	switch mode {
	case "test":
		//dont run mongo query as it is stored in context
		output = &mongo.UpdateResult{}
		err = nil
	case "capture":
		output, err = c.Collection.UpdateMany(ctx, filter, update, opts...)
	default:
		c.log.Error("integrations: Not in a valid sdk mode")
		return nil, errors.New("integrations: Not in a valid sdk mode")
	}

	meta := map[string]string{
		"name":      "mongodb",
		"type":      string(keploy.NoSqlDB),
		"operation": "UpdateMany",
	}

	if err != nil {
		kerr = &keploy.KError{Err: err}
		c.log.Error(err.Error())
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

func (c *MongoDB) DeleteOne(ctx context.Context, filter interface{},
	opts ...*options.DeleteOptions) (*mongo.DeleteResult, error) {
	if keploy.GetMode() == "off" {
		output, err := c.Collection.DeleteOne(ctx, filter, opts...)
		return output, err
	}
	var output *mongo.DeleteResult
	var err error
	var kerr *keploy.KError = &keploy.KError{Err: nil}
	kctx, er := keploy.GetState(ctx)
	if er != nil {
		c.log.Error(er.Error())
		return nil,er
	}
	mode := kctx.Mode
	switch mode {
	case "test":
		//dont run mongo query as it is stored in context
		output = &mongo.DeleteResult{}
		err = nil
	case "capture":
		output, err = c.Collection.DeleteOne(ctx, filter, opts...)
	default:
		c.log.Error("integrations: Not in a valid sdk mode")
		return nil, errors.New("integrations: Not in a valid sdk mode")
	}

	meta := map[string]string{
		"name":      "mongodb",
		"type":      string(keploy.NoSqlDB),
		"operation": "DeleteOne",
	}

	if err != nil {
		kerr = &keploy.KError{Err: err}
		c.log.Error(err.Error())
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

func (c *MongoDB) DeleteMany(ctx context.Context, filter interface{},
	opts ...*options.DeleteOptions) (*mongo.DeleteResult, error) {
	if keploy.GetMode() == "off" {
		output, err := c.Collection.DeleteMany(ctx, filter, opts...)
		return output, err
	}
	var output *mongo.DeleteResult
	var err error
	var kerr *keploy.KError = &keploy.KError{Err: nil}
	kctx, er := keploy.GetState(ctx)
	if er != nil {
		c.log.Error(er.Error())
		return nil,er
	}
	mode := kctx.Mode
	switch mode {
	case "test":
		//dont run mongo query as it is stored in context
		output = &mongo.DeleteResult{}
		err = nil
	case "capture":
		output, err = c.Collection.DeleteMany(ctx, filter, opts...)
	default:
		c.log.Error("integrations: Not in a valid sdk mode")
		return nil, errors.New("integrations: Not in a valid sdk mode")
	}

	meta := map[string]string{
		"name":      "mongodb",
		"type":      string(keploy.NoSqlDB),
		"operation": "DeleteMany",
	}

	if err != nil {
		kerr = &keploy.KError{Err: err}
		c.log.Error(err.Error())
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
