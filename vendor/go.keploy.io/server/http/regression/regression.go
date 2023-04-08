package regression

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi"
	"github.com/go-chi/render"
	"github.com/google/uuid"

	"go.keploy.io/server/graph"
	"go.keploy.io/server/pkg/models"
	regression2 "go.keploy.io/server/pkg/service/regression"

	// "go.keploy.io/server/pkg/service/run"
	tcSvc "go.keploy.io/server/pkg/service/testCase"
	"go.uber.org/zap"
)

func New(r chi.Router, logger *zap.Logger, svc regression2.Service, tc tcSvc.Service, testExport bool, testReportPath string) {
	s := &regression{
		logger:         logger,
		svc:            svc,
		testExport:     testExport,
		testReportPath: testReportPath,
		tcSvc:          tc,
	}

	r.Route("/regression", func(r chi.Router) {
		r.Route("/testcase", func(r chi.Router) {
			r.Get("/{id}", s.GetTC)
			r.Get("/", s.GetTCS)
			r.Post("/", s.PostTC)
		})
		r.Post("/test", s.Test)
		r.Post("/denoise", s.DeNoise)
		r.Get("/start", s.Start)
		r.Get("/end", s.End)

		//r.Get("/search", searchArticles)                                  // GET /articles/search
	})
}

type regression struct {
	testExport     bool
	testReportPath string
	logger         *zap.Logger
	svc            regression2.Service
	tcSvc          tcSvc.Service
}

func (rg *regression) End(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	status := models.TestRunStatus(r.URL.Query().Get("status"))
	stat := models.TestRunStatusFailed
	if status == "true" {
		stat = models.TestRunStatusPassed
	}

	var (
		err error
		now = time.Now().Unix()
	)

	err = rg.svc.PutTest(r.Context(), models.TestRun{
		ID:      id,
		Updated: now,
		Status:  stat,
	}, rg.testExport, id, "", "", rg.testReportPath, 0)
	if err != nil {
		render.Render(w, r, ErrInvalidRequest(err))
		return
	}
	render.Status(r, http.StatusOK)
}

func (rg *regression) Start(w http.ResponseWriter, r *http.Request) {
	t := r.URL.Query().Get("total")
	testCasePath := r.URL.Query().Get("testCasePath")
	mockPath := r.URL.Query().Get("mockPath")
	total, err := strconv.Atoi(t)
	if err != nil {
		render.Render(w, r, ErrInvalidRequest(err))
		return
	}
	app := rg.getMeta(w, r, true)
	if app == "" {
		return
	}
	id := uuid.New().String()
	now := time.Now().Unix()

	err = rg.svc.PutTest(r.Context(), models.TestRun{
		ID:      id,
		Created: now,
		Updated: now,
		Status:  models.TestRunStatusRunning,
		CID:     graph.DEFAULT_COMPANY,
		App:     app,
		User:    graph.DEFAULT_USER,
		Total:   total,
	}, rg.testExport, id, testCasePath, mockPath, rg.testReportPath, total)
	if err != nil {
		render.Render(w, r, ErrInvalidRequest(err))
		return
	}
	render.Status(r, http.StatusOK)
	render.JSON(w, r, map[string]string{
		"id": id,
	})

}

func (rg *regression) GetTC(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	app := rg.getMeta(w, r, false)
	tcs, err := rg.tcSvc.Get(r.Context(), graph.DEFAULT_COMPANY, app, id)
	if err != nil {
		render.Render(w, r, ErrInvalidRequest(err))
		return
	}
	render.Status(r, http.StatusOK)
	render.JSON(w, r, tcs)

}

func (rg *regression) getMeta(w http.ResponseWriter, r *http.Request, appRequired bool) string {
	app := r.URL.Query().Get("app")
	if app == "" && appRequired {
		rg.logger.Error("request for fetching testcases should include app id")
		render.Render(w, r, ErrInvalidRequest(errors.New("missing app id")))
		return ""
	}
	return app
}

func (rg *regression) GetTCS(w http.ResponseWriter, r *http.Request) {
	app := rg.getMeta(w, r, true)
	if app == "" {
		return
	}
	testCasePath := r.URL.Query().Get("testCasePath")
	mockPath := r.URL.Query().Get("mockPath")
	offsetStr := r.URL.Query().Get("offset")
	limitStr := r.URL.Query().Get("limit")
	tcsType := r.URL.Query().Get("reqType")

	var (
		offset int
		limit  int
		err    error
		tcs    []models.TestCase
		eof    bool = rg.testExport
	)
	if offsetStr != "" {
		offset, err = strconv.Atoi(offsetStr)
		if err != nil {
			rg.logger.Error("request for fetching testcases in converting offset to integer")
		}
	}
	if limitStr != "" {
		limit, err = strconv.Atoi(limitStr)
		if err != nil {
			rg.logger.Error("request for fetching testcases in converting limit to integer")
		}
	}

	// fetch all types of testcase
	tcs, err = rg.tcSvc.GetAll(r.Context(), graph.DEFAULT_COMPANY, app, &offset, &limit, testCasePath, mockPath, tcsType)

	if err != nil {
		render.Render(w, r, ErrInvalidRequest(err))
		return
	}
	render.Status(r, http.StatusOK)
	// In test-export, eof is true to stop the infinite for loop in sdk
	w.Header().Set("EOF", fmt.Sprintf("%v", eof))
	render.JSON(w, r, tcs)

}

func (rg *regression) PostTC(w http.ResponseWriter, r *http.Request) {
	data := &models.TestCaseReq{}
	var (
		inserted []string
		err      error
	)
	if err := render.Bind(r, data); err != nil {
		rg.logger.Error("failed to unmarshal testcase in PostTC", zap.Error(err))
		render.Render(w, r, ErrInvalidRequest(err))
		return
	}
	now := time.Now().UTC().Unix()
	inserted, err = rg.tcSvc.Insert(r.Context(), []models.TestCase{{
		ID:       uuid.New().String(),
		Created:  now,
		Updated:  now,
		Captured: data.Captured,
		URI:      data.URI,
		AppID:    data.AppID,
		HttpReq:  data.HttpReq,
		HttpResp: data.HttpResp,
		GrpcReq:  data.GrpcReq,
		GrpcResp: data.GrpcResp,
		Mocks:    data.Mocks,
		Deps:     data.Deps,
		Type:     string(data.Type),
	}}, data.TestCasePath, data.MockPath, graph.DEFAULT_COMPANY, data.Remove, data.Replace)
	if err != nil {
		rg.logger.Error("error putting testcase", zap.Error(err))
		render.Render(w, r, ErrInvalidRequest(err))
		return
	}

	if len(inserted) == 0 {
		rg.logger.Error("unknown failure while inserting testcase")
		render.Render(w, r, ErrInvalidRequest(err))
		return
	}

	render.Status(r, http.StatusOK)
	render.JSON(w, r, map[string]string{"id": inserted[0]})

}

func (rg *regression) DeNoise(w http.ResponseWriter, r *http.Request) {
	// key := r.Header.Get("key")
	// if key == "" {
	// 	rg.logger.Error("missing api key")
	// 	render.Render(w, r, ErrInvalidRequest(errors.New("missing api key")))
	// 	return
	// }

	data := &models.TestReq{}
	var (
		err     error
		body    string
		tcsType string = string(models.HTTP)
	)
	if err = render.Bind(r, data); err != nil {
		rg.logger.Error("error parsing request", zap.Error(err))
		render.Render(w, r, ErrInvalidRequest(err))
		return
	}
	switch data.Type {
	case models.GRPC_EXPORT:
		body = data.GrpcResp.Body
		tcsType = string(models.GRPC_EXPORT)
	default:
		// default tcsType is Http.
		body = data.Resp.Body
		tcsType = string(models.HTTP)
	}

	err = rg.svc.DeNoise(r.Context(), graph.DEFAULT_COMPANY, data.ID, data.AppID, body, data.Resp.Header, data.TestCasePath, tcsType)
	if err != nil {
		rg.logger.Error("error putting testcase", zap.Error(err))
		render.Render(w, r, ErrInvalidRequest(err))
		return
	}

	render.Status(r, http.StatusOK)

}

func (rg *regression) Test(w http.ResponseWriter, r *http.Request) {

	data := &models.TestReq{}
	var (
		pass bool
		err  error
		ctx  context.Context
	)
	if err = render.Bind(r, data); err != nil {
		rg.logger.Error("error parsing request", zap.Error(err))
		render.Render(w, r, ErrInvalidRequest(err))
		return
	}
	ctx = r.Context()
	switch data.Type {
	case models.GRPC_EXPORT:
		pass, err = rg.svc.TestGrpc(ctx, data.GrpcResp, graph.DEFAULT_COMPANY, data.AppID, data.RunID, data.ID, data.TestCasePath, data.MockPath)
	default:
		// default tcsType is Http.
		pass, err = rg.svc.Test(ctx, graph.DEFAULT_COMPANY, data.AppID, data.RunID, data.ID, data.TestCasePath, data.MockPath, data.Resp)
	}

	if err != nil {
		rg.logger.Error("error putting testcase", zap.Error(err))
		render.Render(w, r, ErrInvalidRequest(err))
		return
	}

	render.Status(r, http.StatusOK)
	render.JSON(w, r, map[string]bool{"pass": pass})

}
