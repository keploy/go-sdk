package keploy

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/keploy/go-sdk/internal/keploy"
	"go.keploy.io/server/pkg/models"
	"go.uber.org/zap"
)

type Router interface {
	GetRequest() *http.Request              // GetRequest provides access to the current http request object. Example: echo.Context.Request()
	SetRequest(*http.Request)               // SetRequest sets the http request with given request object parameter.
	GetResponseWriter() http.ResponseWriter // GetResponseWriter returns current ResponseWriter of the http handler.
	SetResponseWriter(http.ResponseWriter)  // SetResponseWriter sets the ResponseWriter of http handler with given parameter.
	Context() context.Context               // Context returns the underlying context of the http.Request.
	Next() error                            // Next is used to call the next handler of the middleware chain.
	GetURLParams() map[string]string        // GetURLParams returns the url parameter as key:value pair.
}

func Middleware(k *Keploy, router Router) error {
	if k == nil || keploy.GetMode() == keploy.MODE_OFF {
		return router.Next()
	}
	writer, r, resBody, reqBody, err := ProcessRequest(router.GetResponseWriter(), router.GetRequest(), k)
	if err != nil {
		return err
	}
	router.SetResponseWriter(writer)
	router.SetRequest(r)

	// Store the responses
	// next.ServeHTTP(w, r)
	err = router.Next()
	status := writer.Status
	body := resBody.String()

	// echo returns code and message as string in error after next handler call
	if err != nil {
		str := err.Error()
		arr := strings.Split(str, ", ")
		for _, j := range arr {
			if strings.Contains(j, "code") {
				s, err := strconv.Atoi(j[5:])
				if err != nil {
					k.Log.Info("failed to convert status code from string to int", zap.Any("code", j))
				}
				status = s
			} else if strings.Contains(j, "message") {
				body = j[8:]
			}
		}
	}
	resp := models.HttpResp{
		//Status
		StatusCode: status,
		Header:     router.GetResponseWriter().Header(),
		Body:       body,
	}

	id := router.GetRequest().Header.Get("KEPLOY_TEST_ID")
	if id != "" {
		response := k.GetResp(id)
		response.Resp = resp
		k.PutResp(id, response)

		// Continue further execution after client call in simulate function
		response.L.Unlock()
		return err
	}

	params := router.GetURLParams()
	CaptureTestcase(k, r, reqBody, resp, params)
	return err
}
