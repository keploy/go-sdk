package keploy

import "net/http"

type TestCaseReq struct {
	Captured int64 `json:"captured" bson:"captured"`
	AppID    string   `json:"app_id" bson:"app_id"`
	HttpReq  HttpReq  `json:"http_req" bson:"http_req"`
	HttpResp HttpResp `json:"http_resp" bson:"http_resp"`
}

type TestCase struct {
	ID       string   `json:"id" bson:"_id"`
	Created  int64    `json:"created" bson:"created"`
	Updated  int64    `json:"updated" bson:"updated"`
	Captured int64 `json:"captured" bson:"captured"`
	CID      string   `json:"cid" bson:"cid"`
	AppID    string   `json:"app_id" bson:"app_id"`
	HttpReq  HttpReq  `json:"http_req" bson:"http_req"`
	HttpResp HttpResp `json:"http_resp" bson:"http_resp"`
}

// type sql database -> postgres, mysql, redshift..

type HttpReq struct {
	Method     Method      `json:"method" bson:"method"`
	ProtoMajor int         `json:"proto_major" bson:"proto_major"` // e.g. 1
	ProtoMinor int         `json:"proto_minor" bson:"proto_minor"` // e.g. 0
	URL        string      `json:"url" bson:"url"`
	Header     http.Header `json:"header" bson:"header"`
	Body       string      `json:"body" bson:"body"`
}

type HttpResp struct {
	Status       string      `json:"status" bson:"status"`           // e.g. "200 OK"
	StatusCode   int         `json:"status_code" bson:"status_code"` // e.g. 200
	ProtoMajor   int         `json:"proto_major" bson:"proto_major"` // e.g. 1
	ProtoMinor   int         `json:"proto_minor" bson:"proto_minor"` // e.g. 0
	Header       http.Header `json:"header" bson:"header"`
	Body         string      `json:"body" bson:"body"`
	Uncompressed bool        `json:"uncompressed" bson:"uncompressed"`
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
