package browserMock

import (
	"net/http"

	"github.com/go-chi/chi"
	"github.com/go-chi/render"
	"go.keploy.io/server/http/regression"
	"go.keploy.io/server/pkg/models"
	mock2 "go.keploy.io/server/pkg/service/browserMock"
	"go.uber.org/zap"
)

func New(r chi.Router, logger *zap.Logger, svc mock2.Service) {
	s := &mock{
		logger: logger,
		svc:    svc,
	}

	r.Route("/deps", func(r chi.Router) {
		r.Get("/", s.Get)
		r.Post("/", s.Post)
	})
}

type mock struct {
	logger *zap.Logger
	svc    mock2.Service
}

func (m *mock) Get(w http.ResponseWriter, r *http.Request) {
	app := r.URL.Query().Get("appid")
	testName := r.URL.Query().Get("testName")
	res, err := m.svc.Get(r.Context(), app, testName)
	if err != nil {
		render.Render(w, r, regression.ErrInvalidRequest(err))
		return
	}
	render.Status(r, http.StatusOK)
	render.JSON(w, r, res)
}

func (m *mock) Post(w http.ResponseWriter, r *http.Request) {
	data := &BrowserMockReq{}
	if err := render.Bind(r, data); err != nil {
		m.logger.Error("error parsing request", zap.Error(err))
		render.Render(w, r, regression.ErrInvalidRequest(err))
		return
	}

	err := m.svc.Put(r.Context(), models.BrowserMock(*data))
	if err != nil {
		render.Render(w, r, regression.ErrInvalidRequest(err))
	}
	return
	// render.Status(r, http.StatusOK)
	// render.JSON(w, r, "Inserted succesfully")
}
