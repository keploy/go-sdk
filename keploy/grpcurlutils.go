package keploy

import (
	"context"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/fullstorydev/grpcurl"
	"github.com/jhump/protoreflect/grpcreflect"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	reflectpb "google.golang.org/grpc/reflection/grpc_reflection_v1alpha"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/runtime/protoiface"
)

const noVersion = "dev build <no version set>"

var ver = noVersion

type multiString []string

func (s *multiString) String() string {
	return strings.Join(*s, ",")
}

func (s *multiString) Set(value string) error {
	*s = append(*s, value)
	return nil
}

type CustomHandler struct {
	*grpcurl.DefaultEventHandler
}

func NewCustomHandler(c *grpcurl.DefaultEventHandler) grpcurl.InvocationEventHandler {
	return &CustomHandler{DefaultEventHandler: c}
}

func (h *CustomHandler) OnReceiveResponse(protoiface.MessageV1) {
}

// GrpCurl function acts as a grpc client for the simulate client.
// It takes grpcRequest json , testcase id, port and request method
// as its parameter
func GrpCurl(grpcReq string, id string, port string, method string) error {
	var (
		isUnixSocket       func() bool // nil when run on non-unix platform
		addlHeaders        multiString
		data               string
		format             string  = "json"
		allowUnknownFields bool    = false
		connectTimeout     float64 = 0
		formatError        bool    = false
		maxMsgSz           int     = 0
		emitDefaults       bool    = false
		reflection                 = optionalBoolFlag{val: true}
	)

	addlHeaders = multiString{id}
	var argsTemp = [2]string{port, method}
	data = grpcReq
	args := argsTemp[0:]

	var (
		target     string
		symbol     string
		cc         *grpc.ClientConn
		descSource grpcurl.DescriptorSource
		refClient  *grpcreflect.Client
		fileSource grpcurl.DescriptorSource
	)
	target = args[0]
	args = args[1:]

	verbosityLevel := 0

	symbol = args[0]

	ctx := context.Background()
	dial := func() *grpc.ClientConn {
		dialTime := 10 * time.Second
		if connectTimeout > 0 {
			dialTime = time.Duration(connectTimeout * float64(time.Second))
		}
		ctx, cancel := context.WithTimeout(ctx, dialTime)
		defer cancel()
		var opts []grpc.DialOption
		if maxMsgSz > 0 {
			opts = append(opts, grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(maxMsgSz)))
		}
		var creds credentials.TransportCredentials

		grpcurlUA := "grpcurl/" + ver
		if ver == noVersion {
			grpcurlUA = "grpcurl/dev-build (no version set)"
		}
		opts = append(opts, grpc.WithUserAgent(grpcurlUA))

		network := "tcp"
		if isUnixSocket != nil && isUnixSocket() {
			network = "unix"
		}
		cc, err := grpcurl.BlockingDial(ctx, network, target, creds, opts...)
		if err != nil {
			// fail(err, "Failed to dial target host %q", target)
			panic(err)
		}
		return cc
	}
	if reflection.val {
		md := grpcurl.MetadataFromHeaders(addlHeaders)
		refCtx := metadata.NewOutgoingContext(ctx, md)
		cc = dial()
		refClient = grpcreflect.NewClientV1Alpha(refCtx, reflectpb.NewServerReflectionClient(cc))
		reflSource := grpcurl.DescriptorSourceFromServer(ctx, refClient)
		descSource = reflSource
	} else {
		descSource = fileSource
	}

	// arrange for the RPCs to be cleanly shutdown
	reset := func() {
		if refClient != nil {
			refClient.Reset()
			refClient = nil
		}
		if cc != nil {
			cc.Close()
			cc = nil
		}
	}
	defer reset()
	// Invoke an RPC
	if cc == nil {
		cc = dial()
	}
	var in io.Reader
	if data == "@" {
		in = os.Stdin
	} else {
		in = strings.NewReader(data)
	}

	// if not verbose output, then also include record delimiters
	// between each message, so output could potentially be piped
	// to another grpcurl process
	includeSeparators := verbosityLevel == 0
	options := grpcurl.FormatOptions{
		EmitJSONDefaultFields: emitDefaults,
		IncludeTextSeparator:  includeSeparators,
		AllowUnknownFields:    allowUnknownFields,
	}
	rf, formatter, err := grpcurl.RequestParserAndFormatter(grpcurl.Format(format), descSource, in, options)
	if err != nil {
		return err
	}

	h := &grpcurl.DefaultEventHandler{
		Out:            os.Stdout,
		Formatter:      formatter,
		VerbosityLevel: verbosityLevel,
	}

	ch := NewCustomHandler(h)
	err = grpcurl.InvokeRPC(ctx, descSource, cc, symbol, addlHeaders, ch, rf.Next)
	if err != nil {
		if errStatus, ok := status.FromError(err); ok && formatError {
			h.Status = errStatus
		} else {
			return err
		}
	}
	return err
}

type optionalBoolFlag struct {
	set, val bool
}

func (f *optionalBoolFlag) Set(s string) error {
	v, err := strconv.ParseBool(s)
	if err != nil {
		return err
	}
	f.set = true
	f.val = v
	return nil
}
