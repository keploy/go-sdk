package integrations

import (
	// "errors"
	"io"

	"github.com/keploy/go-agent/keploy"
	// "github.com/labstack/gommon/log"
	"google.golang.org/grpc"

	"go.uber.org/zap"

	"context"
)

func clientInterceptor(app *keploy.App) func (
	ctx context.Context,
	method string,
	req interface{},
	reply interface{},
	cc *grpc.ClientConn,
	invoker grpc.UnaryInvoker,
	opts ...grpc.CallOption,
) error {
	return func (
		ctx context.Context,
		method string,
		req interface{},
		reply interface{},
		cc *grpc.ClientConn,
		invoker grpc.UnaryInvoker,
		opts ...grpc.CallOption,
	) error {
		// Logic before invoking the invoker

	// Calls the invoker to execute RPC
	var err error
	mode := keploy.GetMode()
	switch mode {
	case "test":
		//dont run invoker
	case "off":
		err = invoker(ctx, method, req, reply, cc, opts...)
		return err
	default:
		err = invoker(ctx, method, req, reply, cc, opts...)
	}

	// Logic after invoking the invoker

	meta := map[string]string{
		"operation": method,
	}

	logger, _ := zap.NewProduction()
	defer logger.Sync()

	mock, res := keploy.ProcessDep(ctx, logger, meta, reply)
	if mock {
		var mockErr error
		if len(res)!=2{
			app.Log.Error("Did not recieve grpc client object")
			return nil
		}
		if res[0] != nil {
			reply = res[0]
		}
		if res[1] != nil {
			mockErr = res[1].(error)
			return mockErr
		}

	}

	return err
	}
}

// func clientInterceptor(
// 	ctx context.Context,
// 	method string,
// 	req interface{},
// 	reply interface{},
// 	cc *grpc.ClientConn,
// 	invoker grpc.UnaryInvoker,
// 	opts ...grpc.CallOption,
// ) error {
// 	// Logic before invoking the invoker

// 	// Calls the invoker to execute RPC
// 	var err error
// 	mode := keploy.GetMode()
// 	switch mode {
// 	case "test":
// 		//dont run invoker
// 	case "off":
// 		err = invoker(ctx, method, req, reply, cc, opts...)
// 		return err
// 	default:
// 		err = invoker(ctx, method, req, reply, cc, opts...)
// 	}

// 	// Logic after invoking the invoker

// 	meta := map[string]string{
// 		"operation": method,
// 	}

// 	logger, _ := zap.NewProduction()
// 	defer logger.Sync()

// 	mock, res := keploy.ProcessDep(ctx, logger, meta, reply)
// 	if mock {
// 		var mockErr error
// 		if len(res)!=2{
			
// 			return 
// 		}
// 		if res[0] != nil {
// 			reply = res[0]
// 		}
// 		if res[1] != nil {
// 			mockErr = res[1].(error)
// 			return mockErr
// 		}

// 	}

// 	return err
// }

func StreamClientInterceptor(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {

	var (
		err          error
		clientStream grpc.ClientStream
		testingMode  bool
	)
	mode := keploy.GetMode()

	switch mode {
	case "test":
		//dont run invoker
		clientStreamAdd := new(grpc.ClientStream)
		clientStream = *clientStreamAdd
		testingMode = true
	case "off":
		clientStream, err = streamer(ctx, desc, cc, method, opts...)
		return clientStream, err
	default:
		clientStream, err = streamer(ctx, desc, cc, method, opts...)
		testingMode = false
	}

	return &tracedClientStream{
		ClientStream: clientStream,
		method:       method,
		context:      ctx,
		testMode:     testingMode,
	}, err

}

type tracedClientStream struct {
	grpc.ClientStream
	method   string
	context  context.Context
	testMode bool
}

func (s *tracedClientStream) RecvMsg(m interface{}) error {
	//test mode
	if s.testMode {
		meta := map[string]string{
			"operation": s.method,
		}

		logger, _ := zap.NewProduction()
		defer logger.Sync()

		mock, res := keploy.ProcessDep(s.context, logger, meta, []interface{}{})

		if mock {
			var mockErr error
			if res[1] != nil {
				mockErr = res[1].(error)
				return mockErr
			}
			m = res[0]
		}
		return nil
	}

	//capture mode
	err := s.ClientStream.RecvMsg(m)

	if err != nil || err == io.EOF {
		return err
	}
	meta := map[string]string{
		"operation": s.method,
	}

	logger, _ := zap.NewProduction()
	defer logger.Sync()

	mock, res := keploy.ProcessDep(s.context, logger, meta, m)

	if mock {
		var mockErr error
		if res[1] != nil {
			mockErr = res[1].(error)
			return mockErr
		}
	}

	return err
}

func WithClientUnaryInterceptor(app *keploy.App) grpc.DialOption {
	return grpc.WithUnaryInterceptor(clientInterceptor(app))
}

func WithClientStreamInterceptor() grpc.DialOption {
	return grpc.WithStreamInterceptor(StreamClientInterceptor)
}
