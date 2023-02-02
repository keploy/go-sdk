package kgrpc

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"go.keploy.io/server/pkg/models"

	"github.com/keploy/go-sdk/keploy"
	internal "github.com/keploy/go-sdk/pkg/keploy"
	"go.uber.org/zap"
	"google.golang.org/grpc"
)

func clientInterceptor(k *keploy.Keploy) func(
	ctx context.Context,
	method string,
	req interface{},
	reply interface{},
	cc *grpc.ClientConn,
	invoker grpc.UnaryInvoker,
	opts ...grpc.CallOption,
) error {
	return func(
		ctx context.Context,
		method string,
		req interface{},
		reply interface{},
		cc *grpc.ClientConn,
		invoker grpc.UnaryInvoker,
		opts ...grpc.CallOption,
	) error {

		if internal.GetModeFromContext(ctx) == internal.MODE_OFF {
			err := invoker(ctx, method, req, reply, cc, opts...)
			return err
		}

		var (
			err  error
			kerr = &keploy.KError{}
		)
		kctx, er := internal.GetState(ctx)
		if er != nil {
			return er
		}

		mode := kctx.Mode
		switch mode {
		case internal.MODE_TEST:
			//dont run invoker
		case internal.MODE_RECORD:
			err = invoker(ctx, method, req, reply, cc, opts...)
		default:
			return errors.New("integrations: Not in a valid sdk mode")
		}

		meta := map[string]string{
			"name":            "gRPC",
			"type":            string(models.GRPC),
			"operation":       method,
			"request":         fmt.Sprint(req),
			"grpc.CallOption": fmt.Sprint(opts),
		}
		if err != nil {
			kerr = &keploy.KError{Err: err}
		}
		mock, res := keploy.ProcessDep(ctx, k.Log, meta, reply, kerr)

		if mock {
			var mockErr error
			if len(res) != 2 {
				k.Log.Error("Did not recieve grpc client object")
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
func streamClientInterceptor(k *keploy.Keploy) func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
	return func(ctx context.Context,
		desc *grpc.StreamDesc,
		cc *grpc.ClientConn,
		method string,
		streamer grpc.Streamer,
		opts ...grpc.CallOption) (grpc.ClientStream, error) {

		if internal.GetModeFromContext(ctx) == internal.MODE_OFF {
			clientStream, err := streamer(ctx, desc, cc, method, opts...)
			return clientStream, err
		}
		var (
			err          error
			clientStream grpc.ClientStream
		)
		kctx, er := internal.GetState(ctx)
		if er != nil {
			emptyCS := new(grpc.ClientStream)
			clientStream = *emptyCS
			return clientStream, er
		}
		mode := kctx.Mode

		switch mode {
		case internal.MODE_TEST:
			//dont run invoker
			clientStreamAdd := new(grpc.ClientStream)
			clientStream = *clientStreamAdd
		case internal.MODE_RECORD:
			clientStream, err = streamer(ctx, desc, cc, method, opts...)
		}

		return &tracedClientStream{
			ClientStream: clientStream,
			method:       method,
			context:      ctx,
			log:          k.Log,
			opts:         opts,
			desc:         *desc,
		}, err
	}

}

type tracedClientStream struct {
	grpc.ClientStream
	method  string
	context context.Context
	log     *zap.Logger
	opts    []grpc.CallOption
	desc    grpc.StreamDesc
}

func (s *tracedClientStream) CloseSend() error {
	var (
		err  error
		kerr = &keploy.KError{}
	)
	kctx, er := internal.GetState(s.context)
	if er != nil {
		return er
	}
	mode := kctx.Mode
	switch mode {
	case internal.MODE_RECORD:
		err = s.ClientStream.CloseSend()
	case internal.MODE_TEST:
		// don't call CloseSend

	}
	if err != nil {
		kerr = &keploy.KError{Err: err}
	}
	meta := map[string]string{
		"name":            "gRPC",
		"type":            string(models.GRPC),
		"operation":       s.method + "/grpc.ClientStream.CloseSend",
		"grpc.StreamDesc": fmt.Sprint(s.desc),
		"grpc.CallOption": fmt.Sprint(s.opts),
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

func (s *tracedClientStream) SendMsg(m interface{}) error {
	var (
		err  error
		kerr = &keploy.KError{}
	)
	kctx, er := internal.GetState(s.context)
	if er != nil {
		return er
	}
	mode := kctx.Mode
	switch mode {
	case internal.MODE_RECORD:
		err = s.ClientStream.SendMsg(m)
	case internal.MODE_TEST:
		// don't call SendMsg

	}
	if err != nil {
		kerr = &keploy.KError{Err: err}
	}
	meta := map[string]string{
		"name":            "gRPC",
		"type":            string(models.GRPC),
		"operation":       s.method + "/grpc.ClientStream.SendMsg",
		"grpc.StreamDesc": fmt.Sprint(s.desc),
		"grpc.CallOption": fmt.Sprint(s.opts),
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

func (s *tracedClientStream) Context() context.Context {

	var ctxOutput context.Context
	kctx, er := internal.GetState(s.context)
	if er != nil {
		return ctxOutput
	}
	mode := kctx.Mode
	switch mode {
	case internal.MODE_RECORD:
		ctxOutput = s.ClientStream.Context()
	case internal.MODE_TEST:
		// don't call Context

	}
	meta := map[string]string{
		"name":            "gRPC",
		"type":            string(models.GRPC),
		"operation":       s.method + "/grpc.ClientStream.Context",
		"grpc.StreamDesc": fmt.Sprint(s.desc),
		"grpc.CallOption": fmt.Sprint(s.opts),
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
		err  error
		kerr = &keploy.KError{}
	)
	kctx, er := internal.GetState(s.context)
	if er != nil {
		return er
	}
	mode := kctx.Mode
	switch mode {
	case internal.MODE_RECORD:
		err = s.ClientStream.RecvMsg(m)
	case internal.MODE_TEST:
		// don't call RecvMsg

	}
	if err != nil {
		kerr = &keploy.KError{Err: err}
	}
	meta := map[string]string{
		"name":            "gRPC",
		"type":            string(models.GRPC),
		"operation":       s.method + "/grpc.ClientStream.RecvMsg",
		"grpc.StreamDesc": fmt.Sprint(s.desc),
		"grpc.CallOption": fmt.Sprint(s.opts),
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
func WithClientUnaryInterceptor(k *keploy.Keploy) grpc.DialOption {
	return grpc.WithUnaryInterceptor(clientInterceptor(k))
}

// WithClientStreamInterceptor function adds streaming interceptor to store its
// response as external dependencies. It should be called in grpc.Dial method.
//
// app parameter is the pointer to app instance of API. It should not be nil.
//
// TODO: Add support for bidirectional streaming.
func WithClientStreamInterceptor(k *keploy.Keploy) grpc.DialOption {
	return grpc.WithStreamInterceptor(streamClientInterceptor(k))
}
