package integrations

import (
	"context"
	"encoding/gob"
	"errors"
	// "reflect"
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

// NewMongoDB creates and returns an instance of MongoDB. 
// cl parameter is pointer of mongo.Collection which have mongo operations as its method
func NewMongoDB(cl *mongo.Collection) *MongoDB {
	gob.Register(primitive.ObjectID{})
	logger, _ := zap.NewProduction()
	defer func(){
		_ = logger.Sync() // flushes buffer, if any
	}()
	return &MongoDB{Collection: *cl, log: logger}
}

// MongoDB countains instance of mongo.Collection to mock mongoDB methods
type MongoDB struct {
	mongo.Collection
	log *zap.Logger
}

// MongoSingleResult countains instance of mongo.SingleResult to mock its methods
type MongoSingleResult struct {
	mongo.SingleResult
	ctx context.Context
	log *zap.Logger
}

// Err method returns error message of mongo.SingleResult if any.
func (msr *MongoSingleResult) Err() error {
	if keploy.GetMode() == "off" {
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
		//dont run mongo query as it is stored in context
	case "capture":
		err = msr.SingleResult.Err()
	default:
		return errors.New("integrations: Not in a valid sdk mode")
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

// Decode method decodes the binary data into mongo Document. Returns error 
func (msr *MongoSingleResult) Decode(v interface{}) error {
	if keploy.GetMode() == "off" {
		err := msr.SingleResult.Decode(v)
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
		//dont run mongo query as it is stored in context
	case "capture":
		err = msr.SingleResult.Decode(v)
	default:
		return errors.New("integrations: Not in a valid sdk mode")
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

// FindOne method creates and returns pointer of MongoSingleResult which containes mocked 
// methods of mongo.SingleResult. Filter parameter should not be nil.
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

// InsertOne method mocks Collection.InsertOne of mongo. Returns pointer of mongo.InsertOneResult
// document parameter should not be nil. It is the document to be inserted into collection.
func (c *MongoDB) InsertOne(ctx context.Context, document interface{},
	opts ...*options.InsertOneOptions) (*mongo.InsertOneResult, error) {
	if keploy.GetMode() == "off" {
		output, err := c.Collection.InsertOne(ctx, document, opts...)
		return output, err
	}
	var output *mongo.InsertOneResult = &mongo.InsertOneResult{}
	var err error
	var kerr *keploy.KError = &keploy.KError{}
	var data []interface{}
	data = append(data, document)
	for _, j := range opts {
		data = append(data, j)
	}
	o, e := c.getOutput(ctx, "InsertOne", data)
	if o != nil {
		output = o.(*mongo.InsertOneResult)
	}
	err = e

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

// MongoCursor contains emedded mongo.Cursor in order to override its methods
type MongoCursor struct {
	mongo.Cursor
	ctx context.Context
	log *zap.Logger
}

// Next function returns boolean that whether the batch is empty or not. returns false if there 
// there is no more document matching with filter.
func (cr *MongoCursor) Next(ctx context.Context) bool {
	if keploy.GetMode() == "off" {
		return cr.Cursor.Next(ctx)
	}
	kctx, er := keploy.GetState(cr.ctx)
	if er != nil {
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

// Decode functions decodes []byte into v parameter.
func (cr *MongoCursor) Decode(v interface{}) error {
	if keploy.GetMode() == "off" {
		err := cr.Cursor.Decode(v)
		return err
	}
	var err error
	var kerr *keploy.KError = &keploy.KError{}
	kctx, er := keploy.GetState(cr.ctx)
	if er != nil {	
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
		return errors.New("integrations: Not in a valid sdk mode")
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
		x := res[1].(*keploy.KError)
		if x.Err != nil {
			mockErr = x.Err
		}
		return mockErr
	}
	return err
}

// Find creates and returns the instance of pointer to MongoCursor which have overridden methods of mongo.Cursor
//
// The filter parameter must be a document containing query operators and can be used to select which documents are
// included in the result. It cannot be nil. An empty document (e.g. bson.D{}) should be used to include all documents.
//
// The opts parameter can be used to specify options for the operation (see the options.FindOptions documentation).
func (c *MongoDB) Find(ctx context.Context, filter interface{},
	opts ...*options.FindOptions) (*MongoCursor, error) {
	if keploy.GetMode() == "off" {
		cursor, err := c.Collection.Find(ctx, filter, opts...)
		return &MongoCursor{
			Cursor: *cursor,
			ctx:    ctx,
			log:    c.log,
		}, err
	}

	kctx, er := keploy.GetState(ctx)
	if er != nil {

		return &MongoCursor{
			log: c.log,
			ctx: ctx,
		}, er
	}
	mode := kctx.Mode
	var (
		cursor *mongo.Cursor
		err    error
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
			Cursor: *cursor,
			log:    c.log,
			ctx:    ctx,
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
	var output *mongo.InsertManyResult = &mongo.InsertManyResult{}
	var err error
	var kerr *keploy.KError = &keploy.KError{}
	var data []interface{}
	data = append(data, documents)
	for _, j := range opts {
		data = append(data, j)
	}
	o, e := c.getOutput(ctx, "InsertMany", data)
	if o != nil {
		output = o.(*mongo.InsertManyResult)
	}
	err = e

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
	var output *mongo.UpdateResult = &mongo.UpdateResult{}
	var err error
	var kerr *keploy.KError = &keploy.KError{}
	var data []interface{}
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
	var output *mongo.UpdateResult = &mongo.UpdateResult{}
	var err error
	var kerr *keploy.KError = &keploy.KError{}
	var data []interface{}
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
	var output *mongo.DeleteResult = &mongo.DeleteResult{}
	var err error
	var kerr *keploy.KError = &keploy.KError{}
	var data []interface{}
	data = append(data, filter)
	for _, j := range opts {
		data = append(data, j)
	}
	o, e := c.getOutput(ctx, "DeleteOne", data)
	if o != nil {
		output = o.(*mongo.DeleteResult)
	}
	err = e

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
	var output *mongo.DeleteResult = &mongo.DeleteResult{}
	var err error
	var kerr *keploy.KError = &keploy.KError{}
	var data []interface{}
	data = append(data, filter)
	for _, j := range opts {
		data = append(data, j)
	}
	o, e := c.getOutput(ctx, "DeleteMany", data)
	if o != nil {
		output = o.(*mongo.DeleteResult)
	}
	err = e

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

func (c *MongoDB) getOutput(ctx context.Context, str string, data []interface{}) (interface{}, error) {
	var (
		output interface{}
		err    error
	)
	kctx, er := keploy.GetState(ctx)
	if er != nil {
		return nil, er
	}
	mode := kctx.Mode
	switch mode {
	case "test":
		//dont run mongo query as it is stored in context
		err = nil
	case "capture":
		output, err = c.callMethod(ctx, str, data)

	default:
		return nil, errors.New("integrations: Not in a valid sdk mode")
	}
	return output, err
}

func (c *MongoDB) callMethod(ctx context.Context, str string, data []interface{}) (interface{}, error) {
	var (
		output interface{}
		err    error
	)
	switch str {
	case "InsertOne":
		doc := data[0]
		data = data[1:]
		var opts []*options.InsertOneOptions
		for _, d := range data {
			opts = append(opts, d.(*options.InsertOneOptions))
		}
		output, err = c.Collection.InsertOne(ctx, doc, opts...)
	case "InsertMany":
		doc := data[0].([]interface{})
		data = data[1:]
		var opts []*options.InsertManyOptions
		for _, d := range data {
			opts = append(opts, d.(*options.InsertManyOptions))
		}
		output, err = c.Collection.InsertMany(ctx, doc, opts...)
	case "UpdateOne":
		filter := data[0]
		update := data[1]
		data = data[2:]
		var opts []*options.UpdateOptions
		for _, d := range data {
			opts = append(opts, d.(*options.UpdateOptions))
		}
		output, err = c.Collection.UpdateOne(ctx, filter, update, opts...)
	case "UpdateMany":
		filter := data[0]
		update := data[1]
		data = data[2:]
		var opts []*options.UpdateOptions
		for _, d := range data {
			opts = append(opts, d.(*options.UpdateOptions))
		}
		output, err = c.Collection.UpdateMany(ctx, filter, update, opts...)
	case "DeleteOne":
		filter := data[0]
		data = data[1:]
		var opts []*options.DeleteOptions
		for _, d := range data {
			opts = append(opts, d.(*options.DeleteOptions))
		}
		output, err = c.Collection.DeleteOne(ctx, filter, opts...)
	case "DeleteMany":
		filter := data[0]
		data = data[1:]
		var opts []*options.DeleteOptions
		for _, d := range data {
			opts = append(opts, d.(*options.DeleteOptions))
		}
		output, err = c.Collection.DeleteMany(ctx, filter, opts...)
	default:
		return nil, errors.New("integerations: SDK Not supported for this method")
	}
	return output, err
}
