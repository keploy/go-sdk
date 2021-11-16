package integrations

import (
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/request"
	"github.com/aws/aws-sdk-go/service/dynamodb"
	"github.com/keploy/go-agent/keploy"
	"go.uber.org/zap"
)

func NewDynamoDB(cl *dynamodb.DynamoDB) * DynamoDB{
	return &DynamoDB{DynamoDB: *cl}
}

type DynamoDB struct {
	dynamodb.DynamoDB
	log *zap.Logger
}

func (c *DynamoDB) QueryWithContext(ctx aws.Context, input *dynamodb.QueryInput, opts ...request.Option) (*dynamodb.QueryOutput, error) {
	output, err := c.DynamoDB.QueryWithContext(ctx, input, opts...)
	if keploy.GetMode() == "off" {
		return output, err
	}
	meta := map[string]string{
		"operation": "QueryWithContext",
	}

	mock, res := keploy.ProcessDep(ctx, c.log, meta, output, err)
	if mock {
		var mockOutput *dynamodb.QueryOutput
		var mockErr error
		if res[0] != nil {
			mockOutput =  res[0].(*dynamodb.QueryOutput)
		}
		if res[1] != nil {
			mockErr =  res[1].(error)
		}
		return mockOutput, mockErr
	}
	return output, err
}

func (c *DynamoDB) GetItemWithContext(ctx aws.Context, input *dynamodb.GetItemInput, opts ...request.Option) (*dynamodb.GetItemOutput, error) {
	output, err := c.DynamoDB.GetItemWithContext(ctx, input, opts...)
	if keploy.GetMode() == "off" {
		return output, err
	}
	meta := map[string]string{
		"operation": "GetItemWithContext",
	}

	mock, res := keploy.ProcessDep(ctx, c.log, meta, output, err)

	if mock {
		var mockOutput *dynamodb.GetItemOutput
		var mockErr error
		if res[0] != nil {
			mockOutput =  res[0].(*dynamodb.GetItemOutput)
		}
		if res[1] != nil {
			mockErr =  res[1].(error)
		}
		return mockOutput, mockErr
	}
	return output, err
}

func (c *DynamoDB) PutItemWithContext(ctx aws.Context, input *dynamodb.PutItemInput, opts ...request.Option) (*dynamodb.PutItemOutput, error) {
	output, err := c.DynamoDB.PutItemWithContext(ctx, input, opts...)
	if keploy.GetMode() == "off" {
		return output, err
	}
	meta := map[string]string{
		"operation": "PutItemWithContext",
	}
	mock, res := keploy.ProcessDep(ctx, c.log, meta, output, err)

	if mock {
		var mockOutput *dynamodb.PutItemOutput
		var mockErr error
		if res[0] != nil {
			mockOutput =  res[0].(*dynamodb.PutItemOutput)
		}
		if res[1] != nil {
			mockErr =  res[1].(error)
		}
		return mockOutput, mockErr
	}
	return output, err
}




