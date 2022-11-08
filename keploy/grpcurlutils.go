package keploy

import (
	"context"
	"flag"
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
)

const noVersion = "dev build <no version set>"

var ver = noVersion

var (
	isUnixSocket func() bool // nil when run on non-unix platform

	flags              = flag.NewFlagSet(os.Args[0], flag.ExitOnError)
	plaintext          = flags.Bool("plaintext", false, "")
	key                = flags.String("key", "", "")
	addlHeaders        multiString
	data               = flags.String("d", "", "")
	format             = flags.String("format", "json", "")
	allowUnknownFields = flags.Bool("allow-unknown-fields", false, "")
	connectTimeout     = flags.Float64("connect-timeout", 0, "")
	formatError        = flags.Bool("format-error", false, "")
	maxMsgSz           = flags.Int("max-msg-sz", 0, "")
	emitDefaults       = flags.Bool("emit-defaults", false, "")
	reflection         = optionalBoolFlag{val: true}
)

func init() {
	flags.Var(&addlHeaders, "H", "")
}

type multiString []string

func (s *multiString) String() string {
	return strings.Join(*s, ",")
}

func (s *multiString) Set(value string) error {
	*s = append(*s, value)
	return nil
}

// Uses a file source as a fallback for resolving symbols and extensions, but
// only uses the reflection source for listing services
type compositeSource struct {
	reflection grpcurl.DescriptorSource
	file       grpcurl.DescriptorSource
}

func (cs compositeSource) ListServices() ([]string, error) {
	return cs.reflection.ListServices()
}

// GrpCurl function acts as a grpc client for the simulate client.
// It takes grpcRequest json , testcase id, port and request method
// as its parameter
func GrpCurl(grpcReq string, id string, port string, method string) error {
	argsArray := [8]string{"grpcurl", "--plaintext", "-d", grpcReq, "-H", id, port, method}
	flags.Parse(argsArray[1:])
	args := flags.Args()
	var target, symbol string
	var cc *grpc.ClientConn
	var descSource grpcurl.DescriptorSource
	var refClient *grpcreflect.Client
	var fileSource grpcurl.DescriptorSource
	target = args[0]
	args = args[1:]

	verbosityLevel := 0

	symbol = args[0]

	ctx := context.Background()
	dial := func() *grpc.ClientConn {
		dialTime := 10 * time.Second
		if *connectTimeout > 0 {
			dialTime = time.Duration(*connectTimeout * float64(time.Second))
		}
		ctx, cancel := context.WithTimeout(ctx, dialTime)
		defer cancel()
		var opts []grpc.DialOption
		if *maxMsgSz > 0 {
			opts = append(opts, grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(*maxMsgSz)))
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
		md := grpcurl.MetadataFromHeaders(append(addlHeaders))
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
	if *data == "@" {
		in = os.Stdin
	} else {
		in = strings.NewReader(*data)
	}

	// if not verbose output, then also include record delimiters
	// between each message, so output could potentially be piped
	// to another grpcurl process
	includeSeparators := verbosityLevel == 0
	options := grpcurl.FormatOptions{
		EmitJSONDefaultFields: *emitDefaults,
		IncludeTextSeparator:  includeSeparators,
		AllowUnknownFields:    *allowUnknownFields,
	}
	rf, formatter, err := grpcurl.RequestParserAndFormatter(grpcurl.Format(*format), descSource, in, options)
	if err != nil {
		return err
	}
	h := &grpcurl.DefaultEventHandler{
		Out:            os.Stdout,
		Formatter:      formatter,
		VerbosityLevel: verbosityLevel,
	}
	err = grpcurl.InvokeRPC(ctx, descSource, cc, symbol, append(addlHeaders), h, rf.Next)
	if err != nil {
		if errStatus, ok := status.FromError(err); ok && *formatError {
			h.Status = errStatus
		} else {
			return err
		}
	}
	return nil
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
