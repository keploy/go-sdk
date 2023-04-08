package models

import (
	"context"

	"gopkg.in/yaml.v3"
)

type Kind string

const (
	V1Beta1 Version = Version("api.keploy.io/v1beta1")
	V1Beta2 Version = Version("api.keploy.io/v1beta2")
)

type Version string

const (
	HTTP           Kind     = "Http"
	GENERIC        Kind     = "Generic"
	SQL            Kind     = "SQL"
	GRPC_EXPORT    Kind     = "gRPC"
	BodyTypeUtf8   BodyType = "utf-8"
	BodyTypeBinary BodyType = "binary"
)

type Mock struct {
	Version Version   `json:"version" yaml:"version"`
	Kind    Kind      `json:"kind" yaml:"kind"`
	Name    string    `json:"name" yaml:"name"`
	Spec    yaml.Node `json:"spec" yaml:"spec"`
}

type GrpcSpec struct {
	Metadata   map[string]string   `json:"metadata" yaml:"metadata"`
	Request    GrpcReq             `json:"grpc_req" yaml:"grpc_req"`
	Response   GrpcResp            `json:"grpc_resp" yaml:"grpc_resp"`
	Objects    []Object            `json:"objects" yaml:"objects"`
	Mocks      []string            `json:"mocks" yaml:"mocks,omitempty"`
	Assertions map[string][]string `json:"assertions" yaml:"assertions,omitempty"`
	Created    int64               `json:"created" yaml:"created,omitempty"`
}

type GrpcReq struct {
	Body   string `json:"body" yaml:"body" bson:"body"`
	Method string `json:"method" yaml:"method" bson:"method"`
}

type GrpcResp struct {
	Body string `json:"body" yaml:"body" bson:"body"`
	Err  string `json:"error" yaml:"error" bson:"error"`
}

type GenericSpec struct {
	Metadata map[string]string `json:"metadata" yaml:"metadata"`
	Objects  []Object          `json:"objects" yaml:"objects"`
}

type Object struct {
	Type string `json:"type" yaml:"type"`
	Data string `json:"data" yaml:"data"`
}

type HttpSpec struct {
	Metadata   map[string]string   `json:"metadata" yaml:"metadata"`
	Request    MockHttpReq         `json:"req" yaml:"req"`
	Response   MockHttpResp        `json:"resp" yaml:"resp"`
	Objects    []Object            `json:"objects" yaml:"objects"`
	Mocks      []string            `json:"mocks" yaml:"mocks,omitempty"`
	Assertions map[string][]string `json:"assertions" yaml:"assertions,omitempty"`
	Created    int64               `json:"created" yaml:"created,omitempty"`
}

type MockHttpReq struct {
	Method     Method            `json:"method" yaml:"method"`
	ProtoMajor int               `json:"proto_major" yaml:"proto_major"` // e.g. 1
	ProtoMinor int               `json:"proto_minor" yaml:"proto_minor"` // e.g. 0
	URL        string            `json:"url" yaml:"url"`
	URLParams  map[string]string `json:"url_params" yaml:"url_params,omitempty"`
	Header     map[string]string `json:"header" yaml:"header"`
	Body       string            `json:"body" yaml:"body"`
	BodyType   string            `json:"body_type" yaml:"body_type"`
	Binary     string            `json:"binary" yaml:"binary,omitempty"`
	Form       []FormData        `json:"form" yaml:"form,omitempty"`
}

type FormData struct {
	Key    string   `json:"key" bson:"key" yaml:"key"`
	Values []string `json:"values" bson:"values,omitempty" yaml:"values,omitempty"`
	Paths  []string `json:"paths" bson:"paths,omitempty" yaml:"paths,omitempty"`
}

type MockHttpResp struct {
	StatusCode    int               `json:"status_code" yaml:"status_code"` // e.g. 200
	Header        map[string]string `json:"header" yaml:"header"`
	Body          string            `json:"body" yaml:"body"`
	BodyType      string            `json:"body_type" yaml:"body_type"`
	StatusMessage string            `json:"status_message" yaml:"status_message"`
	ProtoMajor    int               `json:"proto_major" yaml:"proto_major"`
	ProtoMinor    int               `json:"proto_minor" yaml:"proto_minor"`
	Binary        string            `json:"binary" yaml:"binary,omitempty"`
}

type SQlSpec struct {
	Metadata map[string]string `json:"metadata" yaml:"metadata"`
	Type     SqlOutputType     `json:"type" yaml:"type"` // eg - POST : save data (TABLE) or number of rows affected (INT)
	Table    Table             `json:"table" yaml:"table,omitempty"`
	Int      int               `json:"int" yaml:"int"`
	Err      []string          `json:"error" yaml:"error",omitempty`
}

type Table struct {
	Cols []SqlCol `json:"cols" yaml:"cols"`
	Rows []string `json:"rows" yaml:"rows"`
}

type SqlCol struct {
	Name string `json:"name" yaml:"name"`
	Type string `json:"type" yaml:"type"`
	// optional fields
	Precision int `json:"precision" yaml:"precision"`
	Scale     int `json:"scale" yaml:"scale"`
}

type SqlOutputType string

const (
	TableType SqlOutputType = "table"
	IntType   SqlOutputType = "int"
	ErrType   SqlOutputType = "error"
)

type MockFS interface {
	ReadAll(ctx context.Context, testCasePath, mockPath, tcsType string) ([]TestCase, error)
	Read(ctx context.Context, path, name string, libMode bool) ([]Mock, error)
	Write(ctx context.Context, path string, doc Mock) error
	WriteAll(ctx context.Context, path, fileName string, docs []Mock) error
	Exists(ctx context.Context, path string) bool
}
