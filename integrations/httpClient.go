package integrations

import (
	"bytes"
	"context"

	// "crypto/tls"
	"encoding/gob"
	// "encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"

	// "strings"

	"github.com/keploy/go-sdk/keploy"
	"go.uber.org/zap"
)

// NewHttpClient is used to embed the http.Client pointer in order to mock its methods.
// The purpose is to capture or replay its method's outputs according to "KEPLOY_SDK_MODE".
// It returns nil if client parameter is nil.
//
//  Note: Always call SetCtxHttpClient method of *integrations.HttpClient before using http-client's method. This should be done so that
//        request's context can be modified for storing or retrieving outputs of http methods.
func NewHttpClient(client *http.Client) *HttpClient{
	if client==nil{
		return nil
	}
	gob.Register(ReadCloser{})
	logger, _ := zap.NewProduction()
	defer func(){
		_ = logger.Sync() // flushes buffer, if any
	}()
	return &HttpClient{Client: client, log: logger}
}

// HttpClient used to mock http.Client methods in order to store or retrieve outputs from request's context.
type HttpClient struct{
	*http.Client
	log *zap.Logger
	ctx context.Context
}

// SetCtxHttpClient is used to set integrations.HttpClient.ctx to http.Request.Context(). 
// It should be called before calling any http.Client method so that, their 
// outputs can be stored or retrieved from http.Request.Context() according to "KEPLOY_SDK_MODE".
//
// ctx parameter should be the context of http.Request.  
func (cl *HttpClient)SetCtxHttpClient(ctx context.Context){
	cl.ctx = ctx
}

// ReadCloser is used so that gob could encode-decode http.Response.
type ReadCloser struct {
	*bytes.Reader
	Body io.ReadCloser
}

func (rc ReadCloser) Close() error {
	return nil
}

func (rc *ReadCloser) UnmarshalBinary(b []byte) error {
	rc.Reader = bytes.NewReader(b)
	return nil
}

func (rc *ReadCloser) MarshalBinary() ([]byte, error) {
	if rc.Body!=nil{
		b, err := ioutil.ReadAll(rc.Body)
		rc.Body.Close()
		rc.Reader = bytes.NewReader(b)
		return b, err
	}
	return nil,nil
}

func requestString(req *http.Request) string{
	return fmt.Sprint("Method: ", req.Method, ", URL: ", req.URL, ", Proto: ", req.Proto, ", ProtoMajor: ", req.ProtoMajor, ", ProtoMinor: ", req.ProtoMinor, ", Header: ", req.Header, ", Body: ", req.Body, ", ContentLength: ", req.ContentLength, ", TransferEncoding: ", req.TransferEncoding, ", Close: ", req.Close, ", Host: ", req.Host, ", Form: ", req.Form, ", PostForm: ", req.PostForm, ", MultipartForm: ", req.MultipartForm, ", Trailer: ", req.Trailer, ", RemoteAddr: ", req.RemoteAddr, ", RequestURI: ", req.RequestURI, ", TLS: ", req.TLS, ", Response: ", req.Response, ", Context: ", req.Context())
}

// Do is used to override http.Client's Do method. More about this net/http method: https://pkg.go.dev/net/http#Client.Do.
func (cl *HttpClient) Do(req *http.Request) (*http.Response, error){
	if keploy.GetMode() == "off" {
		resp,err := cl.Client.Do(req)
		return resp,err
	}
	var(
		err error
		kerr *keploy.KError = &keploy.KError{}
		resp *http.Response = &http.Response{}
	) 
	kctx, er := keploy.GetState(cl.ctx)
	if er != nil {
		return nil,er
	}
	mode := kctx.Mode
	body := requestString(req)
	meta := map[string]string{
		"name":      "http-client",
		"type":      string(keploy.HttpClient),
		"operation": "Do",
		"Request":   body,
	}
	switch mode {
	case "test":
		//don't call http.Client.Do method
	case "capture":
		resp, err = cl.Client.Do(req)
	default:
		return nil,errors.New("integrations: Not in a valid sdk mode")
	}

	if err != nil {
		kerr = &keploy.KError{Err: err}
	}	

	resp.Body = &ReadCloser{Body: resp.Body}
	if resp.Request!=nil{
		resp.Request.Body = &ReadCloser{Body: resp.Request.Body}
	}	
	
	mock, res := keploy.ProcessDep(cl.ctx, cl.log, meta, resp, kerr)
	if mock{
		var mockErr error
		x := res[1].(*keploy.KError)
		if x.Err != nil {
			mockErr = x.Err
		}
		return resp,mockErr
	}
	return resp,err
}

// Get mocks the http.Client.Get method of net/http package. More about this net/http method: https://pkg.go.dev/net/http#Client.Get.
func (cl *HttpClient) Get(url string) ( *http.Response, error){
	if keploy.GetMode() == "off" {
		resp,err := cl.Client.Get(url)
		return resp,err
	}
	var(
		err error
		kerr *keploy.KError = &keploy.KError{}
		resp *http.Response = &http.Response{}
	)
	kctx, er := keploy.GetState(cl.ctx)
	if er != nil {
		return nil,er
	}
	mode := kctx.Mode
	meta := map[string]string{
		"name":      "http-client",
		"type":      string(keploy.HttpClient),
		"operation": "Get",
		"URL":   	 url,
	}
	switch mode {
	case "test":
		//don't call http.Client.Get method
	case "capture":
		resp, err = cl.Client.Get(url)
	default:
		return nil,errors.New("integrations: Not in a valid sdk mode")
	}

	if err != nil {
		kerr = &keploy.KError{Err: err}
	}	

	resp.Body = &ReadCloser{Body: resp.Body}
	if resp.Request!=nil{
		resp.Request.Body = &ReadCloser{Body: resp.Request.Body}
	}
	
	mock, res := keploy.ProcessDep(cl.ctx, cl.log, meta, resp, kerr)
	if mock{
		var mockErr error
		x := res[1].(*keploy.KError)
		if x.Err != nil {
			mockErr = x.Err
		}
		return resp,mockErr
	}
	return resp,err
}

// Post mocks the http.Client.Post method. More about this net/http method: https://pkg.go.dev/net/http#Client.Post.
func (cl *HttpClient)  Post(url, contentType string, body io.Reader) (*http.Response, error){
	if keploy.GetMode() == "off" {
		resp,err := cl.Client.Post(url, contentType, body)
		return resp,err
	}
	var(
		err error
		kerr *keploy.KError = &keploy.KError{}
		resp *http.Response = &http.Response{}
	)
	kctx, er := keploy.GetState(cl.ctx)
	if er != nil {
		return nil,er
	}
	mode := kctx.Mode
	meta := map[string]string{
		"name":        "http-client",
		"type":        string(keploy.HttpClient),
		"operation":   "Post",
		"URL":   	   url,
		"ContentType": contentType,
		"body":		   fmt.Sprint(body),
	}
	switch mode {
	case "test":
		//don't call http.Client.Post method
	case "capture":
		resp, err = cl.Client.Post(url, contentType, body)
	default:
		return nil,errors.New("integrations: Not in a valid sdk mode")
	}

	if err != nil {
		kerr = &keploy.KError{Err: err}
	}	

	resp.Body = &ReadCloser{Body: resp.Body}
	if resp.Request!=nil{
		resp.Request.Body = &ReadCloser{Body: resp.Request.Body}
	}
	
	mock, res := keploy.ProcessDep(cl.ctx, cl.log, meta, resp, kerr)
	if mock{
		var mockErr error
		x := res[1].(*keploy.KError)
		if x.Err != nil {
			mockErr = x.Err
		}
		return resp,mockErr
	}
	return resp,err
}

// Head mocks http.Client.Head method. More about this net/http method: https://pkg.go.dev/net/http#Client.Head.
func(cl *HttpClient) Head(url string) ( *http.Response,  error){
	if keploy.GetMode() == "off" {
		resp,err := cl.Client.Head(url)
		return resp,err
	}
	var(
		err error
		kerr *keploy.KError = &keploy.KError{}
		resp *http.Response = &http.Response{}
	)
	kctx, er := keploy.GetState(cl.ctx)
	if er != nil {
		return nil,er
	}
	mode := kctx.Mode
	meta := map[string]string{
		"name":        "http-client",
		"type":        string(keploy.HttpClient),
		"operation":   "Head",
		"URL":   	   url,
	}
	switch mode {
	case "test":
		//don't call http.Client.Head method
	case "capture":
		resp, err = cl.Client.Head(url)
	default:
		return nil,errors.New("integrations: Not in a valid sdk mode")
	}

	if err != nil {
		kerr = &keploy.KError{Err: err}
	}	

	resp.Body = &ReadCloser{Body: resp.Body}	
	if resp.Request!=nil{
		resp.Request.Body = &ReadCloser{Body: resp.Request.Body}
	}
	
	mock, res := keploy.ProcessDep(cl.ctx, cl.log, meta, resp, kerr)
	if mock{
		var mockErr error
		x := res[1].(*keploy.KError)
		if x.Err != nil {
			mockErr = x.Err
		}
		return resp,mockErr
	}
	return resp,err
}

// PostForm Method mocks net/http Client's method. About mocked method: https://pkg.go.dev/net/http#Client.PostForm. 
func (cl *HttpClient) PostForm(url string, data url.Values) ( *http.Response,  error){
	if keploy.GetMode() == "off" {
		resp,err := cl.Client.PostForm(url, data)
		return resp,err
	}
	var(
		err error
		kerr *keploy.KError = &keploy.KError{}
		resp *http.Response = &http.Response{}
	)
	kctx, er := keploy.GetState(cl.ctx)
	if er != nil {
		return nil,er
	}
	mode := kctx.Mode
	meta := map[string]string{
		"name":        "http-client",
		"type":        string(keploy.HttpClient),
		"operation":   "PostForm",
		"URL":   	   url,
		"Data":		   fmt.Sprint(data),
	}
	switch mode {
	case "test":
		//don't call http.Client.PostForm method
	case "capture":
		resp, err = cl.Client.PostForm(url, data)
	default:
		return nil,errors.New("integrations: Not in a valid sdk mode")
	}

	if err != nil {
		kerr = &keploy.KError{Err: err}
	}	

	resp.Body = &ReadCloser{Body: resp.Body}	
	if resp.Request!=nil{
		resp.Request.Body = &ReadCloser{Body: resp.Request.Body}
	}
	
	mock, res := keploy.ProcessDep(cl.ctx, cl.log, meta, resp, kerr)
	if mock{
		var mockErr error
		x := res[1].(*keploy.KError)
		if x.Err != nil {
			mockErr = x.Err
		}
		return resp,mockErr
	}
	return resp,err	
}