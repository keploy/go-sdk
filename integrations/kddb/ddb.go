package kddb

import (
	"errors"
	"go.keploy.io/server/pkg/models"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/request"
	"github.com/aws/aws-sdk-go/service/dynamodb"
	"github.com/keploy/go-sdk/keploy"
	internal "github.com/keploy/go-sdk/pkg/keploy"

	"go.uber.org/zap"
)

func NewDynamoDB(cl *dynamodb.DynamoDB) *DynamoDB {
	logger, _ := zap.NewProduction()
	defer func() {
		_ = logger.Sync() // flushes buffer, if any
	}()
	return &DynamoDB{
		DynamoDB: *cl,
		log:      logger,
	}
}

type DynamoDB struct {
	dynamodb.DynamoDB
	log *zap.Logger
}

func (c *DynamoDB) QueryWithContext(ctx aws.Context, input *dynamodb.QueryInput, opts ...request.Option) (*dynamodb.QueryOutput, error) {

	if internal.GetModeFromContext(ctx) == internal.MODE_OFF {
		return c.DynamoDB.QueryWithContext(ctx, input, opts...)
	}
	output, err := &dynamodb.QueryOutput{}, errors.New("")

	if internal.GetModeFromContext(ctx) != internal.MODE_TEST {
		output, err = c.DynamoDB.QueryWithContext(ctx, input, opts...)
	}

	meta := map[string]string{
		"name":      "dynamodb",
		"type":      string(models.NoSqlDB),
		"operation": "QueryWithContext",
		"query":     input.String(),
	}

	if input.TableName != nil {
		meta["table"] = *input.TableName
	}

	mock, res := keploy.ProcessDep(ctx, c.log, meta, output, err)
	if mock {
		var mockOutput *dynamodb.QueryOutput
		var mockErr error
		if res[0] != nil {
			mockOutput = res[0].(*dynamodb.QueryOutput)
		}
		if res[1] != nil {
			mockErr = res[1].(error)
		}
		return mockOutput, mockErr
	}
	return output, err
}

func (c *DynamoDB) GetItemWithContext(ctx aws.Context, input *dynamodb.GetItemInput, opts ...request.Option) (*dynamodb.GetItemOutput, error) {

	if internal.GetModeFromContext(ctx) == internal.MODE_OFF {
		return c.DynamoDB.GetItemWithContext(ctx, input, opts...)
	}

	output, err := &dynamodb.GetItemOutput{}, errors.New("")

	if internal.GetModeFromContext(ctx) != internal.MODE_TEST {
		output, err = c.DynamoDB.GetItemWithContext(ctx, input, opts...)
	}

	meta := map[string]string{
		"name":      "dynamodb",
		"type":      string(models.NoSqlDB),
		"operation": "GetItemWithContext",
		"query":     input.String(),
	}

	if input.TableName != nil {
		meta["table"] = *input.TableName

	}

	mock, res := keploy.ProcessDep(ctx, c.log, meta, output, err)

	if mock {
		var mockOutput *dynamodb.GetItemOutput
		var mockErr error
		if res[0] != nil {
			mockOutput = res[0].(*dynamodb.GetItemOutput)
		}
		if res[1] != nil {
			mockErr = res[1].(error)
		}
		return mockOutput, mockErr
	}
	return output, err
}

func (c *DynamoDB) PutItemWithContext(ctx aws.Context, input *dynamodb.PutItemInput, opts ...request.Option) (*dynamodb.PutItemOutput, error) {

	if internal.GetModeFromContext(ctx) == internal.MODE_OFF {
		return c.DynamoDB.PutItemWithContext(ctx, input, opts...)
	}

	output, err := &dynamodb.PutItemOutput{}, errors.New("")

	if internal.GetModeFromContext(ctx) != internal.MODE_TEST {
		output, err = c.DynamoDB.PutItemWithContext(ctx, input, opts...)
	}

	meta := map[string]string{
		"name":      "dynamodb",
		"type":      string(models.NoSqlDB),
		"operation": "PutItemWithContext",
		"query":     input.String(),
	}

	if input.TableName != nil {
		meta["table"] = *input.TableName
	}

	mock, res := keploy.ProcessDep(ctx, c.log, meta, output, err)

	if mock {
		var mockOutput *dynamodb.PutItemOutput
		var mockErr error
		if res[0] != nil {
			mockOutput = res[0].(*dynamodb.PutItemOutput)
		}
		if res[1] != nil {
			mockErr = res[1].(error)
		}
		return mockOutput, mockErr
	}
	return output, err
}
