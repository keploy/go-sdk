package keploy

import (
	"context"
	"net/http"

	"go.keploy.io/server/pkg/models"
)

type Router interface {
	GetRequest() *http.Request
	SetRequest(*http.Request)
	GetResponseWriter() http.ResponseWriter
	SetResponseWriter(http.ResponseWriter)
	Context() context.Context
	Next() error
	GetURLParams() map[string]string
}

func Middleware(k *Keploy, router Router) error {
	if k == nil || GetMode() == MODE_OFF {
		return nil
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
