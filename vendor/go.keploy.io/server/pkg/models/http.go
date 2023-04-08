package models

import "net/http"

type HttpReq struct {
	Method     Method            `json:"method" bson:"method,omitempty" yaml:"method"`
	ProtoMajor int               `json:"proto_major" bson:"proto_major,omitempty" yaml:"proto_major"` // e.g. 1
	ProtoMinor int               `json:"proto_minor" bson:"proto_minor,omitempty" yaml:"proto_minor"` // e.g. 0
	URL        string            `json:"url" bson:"url,omitempty" yaml:"url"`
	URLParams  map[string]string `json:"url_params" bson:"url_params,omitempty" yaml:"url_params,omitempty"`
	Header     http.Header       `json:"header" bson:"header,omitempty" yaml:"headers"`
	Body       string            `json:"body" bson:"body,omitempty" yaml:"body"`
	Binary     string            `json:"binary" bson:"binary,omitempty" yaml:"binary,omitempty"`
	Form       []FormData        `json:"form" bson:"form,omitempty" yaml:"form,omitempty"`
}

type HttpResp struct {
	StatusCode int         `json:"status_code" bson:"status_code,omitempty" yaml:"status_code"` // e.g. 200
	Header     http.Header `json:"header" bson:"header,omitempty" yaml:"headers"`
	Body       string      `json:"body" bson:"body,omitempty" yaml:"body"`
	StatusMessage string  		`json:"status_message" yaml:"status_message"`
	ProtoMajor int 				`json:"proto_major" yaml:"proto_major"`
	ProtoMinor int 				`json:"proto_minor" yaml:"proto_minor"`
	Binary        string      `json:"binary" bson:"binary,omitempty" yaml:"binary,omitempty"`
}

type Method string

const (
	MethodGet     Method = "GET"
	MethodPut            = "PUT"
	MethodHead           = "HEAD"
	MethodPost           = "POST"
	MethodPatch          = "PATCH" // RFC 5789
	MethodDelete         = "DELETE"
	MethodOptions        = "OPTIONS"
	MethodTrace          = "TRACE"
)
