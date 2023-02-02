package kmongo

import (
	"context"
	"errors"

	internal "github.com/keploy/go-sdk/pkg/keploy"

	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// FindOne method creates and returns pointer of SingleResult which containes mongo.SingleResult
// in order to mock its method. It mocks Collection.FindOne method explained above in integrations.NewCollections.
//
// See https://pkg.go.dev/go.mongodb.org/mongo-driver/mongo#Collection.FindOne for information about Collection.FindOne.
func (c *Collection) FindOne(ctx context.Context, filter interface{}, opts ...*options.FindOneOptions) *SingleResult {

	derivedOpts := []options.FindOneOptions{}
	for _, j := range opts {
		derivedOpts = append(derivedOpts, *j)
	}
	var singleResult = &SingleResult{
		filter: filter,
		opts:   derivedOpts,
		log:    c.log,
		ctx:    ctx,
	}

	if internal.GetModeFromContext(ctx) == internal.MODE_OFF {
		sr := c.Collection.FindOne(ctx, filter, opts...)
		if sr != nil {
			singleResult.SingleResult = *sr
		}
		return singleResult
	}

	kctx, err := internal.GetState(ctx)
	if err != nil {
		return singleResult
	}
	mode := kctx.Mode
	var sr *mongo.SingleResult

	switch mode {
	case internal.MODE_TEST:
		return singleResult
	case internal.MODE_RECORD:
		sr = c.Collection.FindOne(ctx, filter, opts...)
		if sr != nil {
			singleResult.SingleResult = *sr
		}
	default:
		return singleResult
	}

	return singleResult
}

// Find creates and returns the instance of pointer to  keploy Cursor struct which have overridden methods of mongo.Cursor.
// Actual Collection.Find is called only in keploy.MODE_RECORD or "off" mode.
//
// For information about Collection.Find, See https://pkg.go.dev/go.mongodb.org/mongo-driver/mongo#Collection.Find.
func (c *Collection) Find(ctx context.Context, filter interface{},
	opts ...*options.FindOptions) (*Cursor, error) {

	derivedOpts := []options.FindOptions{}
	for _, j := range opts {
		derivedOpts = append(derivedOpts, *j)
	}
	var result = &Cursor{
		filter:   filter,
		findOpts: derivedOpts,
		log:      c.log,
		ctx:      ctx,
	}

	if internal.GetModeFromContext(ctx) == internal.MODE_OFF {
		cursor, err := c.Collection.Find(ctx, filter, opts...)
		if cursor != nil {
			result.Cursor = *cursor
		}
		return result, err
	}

	kctx, er := internal.GetState(ctx)
	if er != nil {
		return result, er
	}
	mode := kctx.Mode
	var (
		cursor *mongo.Cursor
		err    error
	)

	switch mode {
	case internal.MODE_TEST:
		//don't call method in test mode
		return result, err
	case internal.MODE_RECORD:
		cursor, err = c.Collection.Find(ctx, filter, opts...)
		if cursor != nil {
			result.Cursor = *cursor
		}
		return result, err
	default:
		// c.log.Error("integrations: Not in a valid sdk mode")
		return result, errors.New("integrations: Not in a valid sdk mode")
	}

}
