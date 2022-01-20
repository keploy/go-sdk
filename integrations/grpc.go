package integrations

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"github.com/keploy/go-sdk/keploy"
	"go.uber.org/zap"
	"google.golang.org/grpc"
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

		var (
			err error
			kerr *keploy.KError = &keploy.KError{}
		)
		kctx,er := keploy.GetState(ctx)
		if er!=nil{
			return er
		}

		mode := kctx.Mode
		switch mode {
		case "test":
			//dont run invoker
		case "capture":
			err = invoker(ctx, method, req, reply, cc, opts...)
		default:
			return errors.New("integrations: Not in a valid sdk mode")
		}

		meta := map[string]string{
			"name":      		"gRPC",
			"type":      		string(keploy.RPC),
			"operation":	 	method,
			"request": 	 		fmt.Sprint(req),
			"grpc.CallOption":  fmt.Sprint(opts),
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

// TODO: Add support to use a go routine in bidirectional streaming.
func streamClientInterceptor(app *keploy.App) func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
		return func(ctx context.Context,
			desc *grpc.StreamDesc,
			cc *grpc.ClientConn,
			method string, 
			streamer grpc.Streamer, 
			opts ...grpc.CallOption) (grpc.ClientStream, error){
				
				if keploy.GetMode()=="off"{
					clientStream, err := streamer(ctx, desc, cc, method, opts...)
					return clientStream, err
				}
				var (
					err          error
					clientStream grpc.ClientStream
				)
				kctx,er := keploy.GetState(ctx)
				if er!=nil{
					emptyCS := new(grpc.ClientStream)
					clientStream = *emptyCS
					return clientStream, er
				}
				mode := kctx.Mode
				
				switch mode {
				case "test":
					//dont run invoker
					clientStreamAdd := new(grpc.ClientStream)
					clientStream = *clientStreamAdd
				case "capture":
					clientStream, err = streamer(ctx, desc, cc, method, opts...)
				}
			
				return &tracedClientStream{
					ClientStream: clientStream,
					method:       method,
					context:      ctx,
					log: 		  app.Log,
					opts: 		  opts,
					desc: 		  *desc,	
				}, err
			}
	
}

type tracedClientStream struct {
	grpc.ClientStream
	method   string
	context  context.Context
	log 	 *zap.Logger
	opts 	 []grpc.CallOption
	desc 	 grpc.StreamDesc
}

func (s *tracedClientStream) CloseSend() error{
	var (
		err error
		kerr *keploy.KError = &keploy.KError{}
	)
	kctx,er := keploy.GetState(s.context)
	if er!=nil{
		return er
	}
	mode := kctx.Mode
	switch mode{
	case "capture":
		err = s.ClientStream.CloseSend()
	case "test":
		// don't call CloseSend
	
	}
	if err != nil{
		kerr = &keploy.KError{Err: err}
	}
	meta := map[string]string{
		"name":      		"gRPC",
		"type":      		string(keploy.RPC),
		"operation": 		s.method+"/grpc.ClientStream.CloseSend",
		"grpc.StreamDesc":  fmt.Sprint(s.desc),
		"grpc.CallOption":  fmt.Sprint(s.opts),
	}

	mock, res := keploy.ProcessDep(s.context, s.log, meta, kerr)
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

func (s *tracedClientStream) SendMsg(m interface{}) error{
	var (
		err error
		kerr *keploy.KError = &keploy.KError{}
	)
	kctx,er := keploy.GetState(s.context)
	if er!=nil{
		return er
	}
	mode := kctx.Mode
	switch mode{
	case "capture":
		err = s.ClientStream.SendMsg(m)
	case "test":
		// don't call SendMsg
	
	}
	if err != nil{
		kerr = &keploy.KError{Err: err}
	}
	meta := map[string]string{
		"name":      		"gRPC",
		"type":      		string(keploy.RPC),
		"operation": 		s.method+"/grpc.ClientStream.SendMsg",
		"grpc.StreamDesc":  fmt.Sprint(s.desc),
		"grpc.CallOption":  fmt.Sprint(s.opts),
	}

	mock, res := keploy.ProcessDep(s.context, s.log, meta, m, kerr)
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

func (s *tracedClientStream) Context() context.Context{

	var ctxOutput context.Context
	kctx,er := keploy.GetState(s.context)
	if er!=nil{
		return ctxOutput
	}
	mode := kctx.Mode
	switch mode{
	case "capture":
		ctxOutput = s.ClientStream.Context()
	case "test":
		// don't call Context
	
	}
	meta := map[string]string{
		"name":      		"gRPC",
		"type":      		string(keploy.RPC),
		"operation": 		s.method+"/grpc.ClientStream.Context",
		"grpc.StreamDesc":  fmt.Sprint(s.desc),
		"grpc.CallOption":  fmt.Sprint(s.opts),
	}

	mock, res := keploy.ProcessDep(s.context, s.log, meta, ctxOutput)
	if mock {
		m := context.TODO()
		rm := reflect.ValueOf(m)
		rm.Elem().Set(reflect.ValueOf(res[0]).Elem())
		return m
	}

	return ctxOutput
}

func (s *tracedClientStream) RecvMsg(m interface{}) error {
	
	var (
		err error
		kerr *keploy.KError = &keploy.KError{}
	)
	kctx,er := keploy.GetState(s.context)
	if er!=nil{
		return er
	}
	mode := kctx.Mode
	switch mode{
	case "capture":
		err = s.ClientStream.RecvMsg(m)
	case "test":
		// don't call RecvMsg
	
	}
	if err != nil{
		kerr = &keploy.KError{Err: err}
	}
	meta := map[string]string{
		"name":      		"gRPC",
		"type":      		string(keploy.RPC),
		"operation": 		s.method+"/grpc.ClientStream.RecvMsg",
		"grpc.StreamDesc":  fmt.Sprint(s.desc),
		"grpc.CallOption":  fmt.Sprint(s.opts),
	}

	mock, res := keploy.ProcessDep(s.context, s.log, meta, m, kerr)
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

// WithClientUnaryInterceptor function adds unary client interceptor to store its response as 
// external dependencies. It should be called in grpc.Dial method.
//
// app parameter is the pointer to app instance of API. It should not be nil.
func WithClientUnaryInterceptor(app *keploy.App) grpc.DialOption {
	return grpc.WithUnaryInterceptor(clientInterceptor(app))
}

// WithClientStreamInterceptor function adds streaming interceptor to store its 
// response as external dependencies. It should be called in grpc.Dial method.
//
// app parameter is the pointer to app instance of API. It should not be nil.
//
// TODO: Add support for bidirectional streaming.
func WithClientStreamInterceptor(app *keploy.App) grpc.DialOption {
	return grpc.WithStreamInterceptor(streamClientInterceptor(app))
}
