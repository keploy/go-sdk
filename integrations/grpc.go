package integrations

import (
	"errors"
	"reflect"
	"io"
	"log"
	"github.com/keploy/go-sdk/keploy"
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
	if keploy.GetMode()=="off"{
		err := invoker(ctx, method, req, reply, cc, opts...)
		return err
	}
	var err error
	var kerr *keploy.KError = &keploy.KError{Err: nil}
	kctx,er := keploy.GetState(ctx)
	if er!=nil{
		app.Log.Error(er.Error())
		return er
	}
	mode := kctx.Mode
	switch mode {
	case "test":
		//dont run invoker
	case "capture":
		err = invoker(ctx, method, req, reply, cc, opts...)
	default:
		app.Log.Error("integrations: Not in a valid sdk mode")
		return  errors.New("integrations: Not in a valid sdk mode")
	}

	meta := map[string]string{
		"name":      "gRPC",
		"type":      string(keploy.RPC),
		"operation": method,
	}

	if err!=nil{
		kerr = &keploy.KError{Err: err}
	}
	mock, res := keploy.ProcessDep(ctx, app.Log, meta, reply, kerr)
	if mock {
		var mockErr error
		if len(res)!=2{
			app.Log.Error("Did not recieve grpc client object")
			return nil
		}
		x := res[1].(*keploy.KError)
		if x.Err != nil {
			mockErr = x.Err
		}
		return mockErr

	}

	return err
	}
}

//not added KError for encoding and decoding
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
		defer func(){
			err := logger.Sync() // 
			if err!=nil{
				log.Fatal(err)
			}
		}()

		mock, res := keploy.ProcessDep(s.context, logger, meta, []interface{}{})

		if mock {
			var mockErr error
			if res[1] != nil {
				mockErr = res[1].(error)
				return mockErr
			}
			rm := reflect.ValueOf(m)
			rm.Elem().Set(reflect.ValueOf(res[0]).Elem())
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
	defer func(){
		_ = logger.Sync() // flushes buffer, if any
	}()

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

// WithClientUnaryInterceptor function adds unary client interceptor to store its request, 
// response and external dependencies. It should be called in grpc.Dial method
//
// app parameter is pointer to app instance of API. It should not be nil.
func WithClientUnaryInterceptor(app *keploy.App) grpc.DialOption {
	return grpc.WithUnaryInterceptor(clientInterceptor(app))
}

	// "errors"
func WithClientStreamInterceptor() grpc.DialOption {
	return grpc.WithStreamInterceptor(StreamClientInterceptor)
}
