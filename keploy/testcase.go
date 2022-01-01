package keploy

import "net/http"

type TestReq struct {
	ID    string   `json:"id" bson:"_id"`
	AppID string   `json:"app_id" bson:"app_id"`
	RunID string   `json:"run_id" bson:"run_id"`
	Resp  HttpResp `json:"resp" bson:"resp"`
}

type TestCaseReq struct {
	Captured int64        `json:"captured" bson:"captured"`
	AppID    string       `json:"app_id" bson:"app_id"`
	URI      string       `json:"uri" bson:"uri"`
	HttpReq  HttpReq      `json:"http_req" bson:"http_req"`
	HttpResp HttpResp     `json:"http_resp" bson:"http_resp"`
	Deps     []Dependency `json:"deps" bson:"deps"`
}

type TestCase struct {
	ID       string              `json:"id" bson:"_id"`
	Created  int64               `json:"created" bson:"created"`
	Updated  int64               `json:"updated" bson:"updated"`
	Captured int64               `json:"captured" bson:"captured"`
	CID      string              `json:"cid" bson:"cid"`
	AppID    string              `json:"app_id" bson:"app_id"`
	URI      string              `json:"uri" bson:"uri"`
	HttpReq  HttpReq             `json:"http_req" bson:"http_req"`
	HttpResp HttpResp            `json:"http_resp" bson:"http_resp"`
	Deps     []Dependency        `json:"deps" bson:"deps"`
	AllKeys  map[string][]string `json:"all_keys" bson:"all_keys"`
	Anchors  map[string][]string `json:"anchors" bson:"anchors"`
}

type Dependency struct {
	Name string            `json:"name" bson:"name"`
	Type DependencyType    `json:"type" bson:"type"`
	Meta map[string]string `json:"meta" bson:"meta"`
	Data [][]byte          `json:"data" bson:"data"`
}

type DependencyType string

const(
	NoSqlDB DependencyType = "DB"
	SqlDB   DependencyType = "SQL_DB"
)

// type sql database -> postgres, mysql, redshift..

type HttpReq struct {
	Method     Method            `json:"method" bson:"method"`
	ProtoMajor int               `json:"proto_major" bson:"proto_major"` // e.g. 1
	ProtoMinor int               `json:"proto_minor" bson:"proto_minor"` // e.g. 0
	URLParams  map[string]string `json:"url_params" bson:"url_params"`
	Header     http.Header       `json:"header" bson:"header"`
	Body       string            `json:"body" bson:"body"`
}

type HttpResp struct {
	StatusCode int         `json:"status_code" bson:"status_code"` // e.g. 200
	Header     http.Header `json:"header" bson:"header"`
	Body       string      `json:"body" bson:"body"`
}

type Method string

const (
	MethodGet     Method = "GET"
	MethodPut     Method = "PUT"
	MethodHead    Method = "HEAD"
	MethodPost    Method = "POST"
	MethodPatch   Method = "PATCH" // RFC 5789
	MethodDelete  Method = "DELETE"
	MethodOptions Method = "OPTIONS"
	MethodTrace   Method = "TRACE"
)
