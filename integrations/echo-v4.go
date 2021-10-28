package integrations

import (
	"github.com/keploy/go-agent/keploy"
	"github.com/labstack/echo/v4"
)

func Start(app keploy.App, e *echo.Echo, host, port string)  {
	// start testing process
	e.Logger.Fatal(e.Start(host + ":" + port))
}