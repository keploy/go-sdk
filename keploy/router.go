package keploy

import (
	"context"
	"net/http"

	"go.keploy.io/server/pkg/models"
)

type Router interface {
	// GetRequest provides access to the current http request object. 
	// Example: echo.Context.Request()
	GetRequest() *http.Request
	// SetRequest sets the http request with given request object parameter.
	SetRequest(*http.Request)
	// GetResponseWriter returns current ResponseWriter of the http handler.
	GetResponseWriter() http.ResponseWriter
	// SetResponseWriter sets the ResponseWriter of http handler with given parameter.
	SetResponseWriter(http.ResponseWriter)
	// Context returns the underlying context of the http.Request.
	Context() context.Context
	// Next is used to call the next handler of the middleware chain.
	Next() error
	// GetURLParams returns the url parameter as key:value pair.
	GetURLParams() map[string]string
}

func Middleware(k *Keploy, router Router) error {
	if k == nil || GetMode() == MODE_OFF {
		return router.Next()
	}
	writer, r, resBody, reqBody, err := ProcessRequest(router.GetResponseWriter(), router.GetRequest(), k)
	if err != nil {
		return err
	}
	// w = writer
	router.SetResponseWriter(writer)
	router.SetRequest(r)

	// Store the responses
	// next.ServeHTTP(w, r)
	err = router.Next()
	if err != nil {
		return err
	}
	resp := models.HttpResp{
		//Status
		StatusCode: writer.Status,
		Header:     router.GetResponseWriter().Header(),
		Body:       resBody.String(),
	}

	id := router.GetRequest().Header.Get("KEPLOY_TEST_ID")
	if id != "" {
		k.PutResp(id, resp)
		return nil
	}

	params := router.GetURLParams()
	CaptureTestcase(k, r, reqBody, resp, params)
	return nil
}
