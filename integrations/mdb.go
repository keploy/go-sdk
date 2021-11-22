package integrations

import (
	"context"
	"encoding/gob"
	"fmt"

	"github.com/keploy/go-agent/keploy"
	"go.uber.org/zap"

	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func NewMongoDB(cl *mongo.Collection ) * MongoDB{
	gob.Register(primitive.ObjectID{})
	logger, _ := zap.NewProduction()
	defer logger.Sync()
	return &MongoDB{Collection: *cl, log : logger}
}

type MongoDB struct {
	mongo.Collection
	log *zap.Logger
}

func (c *MongoDB) FindOne(ctx context.Context, filter interface{}, opts ...*options.FindOneOptions) *mongo.SingleResult{
	output := c.Collection.FindOne(ctx, filter, opts...)
	if keploy.GetMode() == "off" {
		return output
	}
	meta := map[string]string{
		"operation": "FindOne",
	}

	mock, res := keploy.ProcessDep(ctx, c.log, meta, output)
	if mock {
		var mockOutput *mongo.SingleResult
		// var mockErr error
		if res[0] != nil {
			mockOutput =  res[0].(*mongo.SingleResult)
		}
		// if res[1] != nil {
		// 	mockErr =  res[1].(error)
		// }
		return mockOutput
	}
	return output
}

func (c *MongoDB) InsertOne(ctx context.Context, document interface{},
	opts ...*options.InsertOneOptions) (*mongo.InsertOneResult, error){
	fmt.Println("ritik")
	output, err := c.Collection.InsertOne(ctx, document, opts...)
	if keploy.GetMode() == "off" {
		return output, err
	}
	meta := map[string]string{
		"operation": "FindOne",
	}
	
	mock, res := keploy.ProcessDep(ctx, c.log, meta, output, err)
	fmt.Println("ritik")
	if mock {
		var mockOutput *mongo.InsertOneResult
		var mockErr error
		if res[0] != nil {
			mockOutput =  res[0].(*mongo.InsertOneResult)
		}
		if res[1] != nil {
			mockErr =  res[1].(error)
		}
		return mockOutput, mockErr
	}
	return output, err
}
