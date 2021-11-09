package integrations

import (
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/request"
	"github.com/aws/aws-sdk-go/service/dynamodb"
)

func NewDynamoDB(cl *dynamodb.DynamoDB) * DynamoDB{
	return &DynamoDB{DynamoDB: *cl}
}

type DynamoDB struct {
	dynamodb.DynamoDB
}

func (c *DynamoDB) QueryWithContext(ctx aws.Context, input *dynamodb.QueryInput, opts ...request.Option) (*dynamodb.QueryOutput, error) {

	return c.DynamoDB.QueryWithContext(ctx, input, opts...)
}

func (c *DynamoDB) GetItemWithContext(ctx aws.Context, input *dynamodb.GetItemInput, opts ...request.Option) (*dynamodb.GetItemOutput, error) {

	return c.DynamoDB.GetItemWithContext(ctx, input, opts...)
}

func (c *DynamoDB) PutItemWithContext(ctx aws.Context, input *dynamodb.PutItemInput, opts ...request.Option) (*dynamodb.PutItemOutput, error) {
	return c.DynamoDB.PutItemWithContext(ctx, input, opts...)
}




