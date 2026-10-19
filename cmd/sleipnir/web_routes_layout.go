package main

import (
	"github.com/anemos-labs/sleipnir/internal/web"
	"github.com/anemos-labs/sleipnir/internal/web/layout"
	"github.com/anemos-labs/sleipnir/internal/web/seam"
)

// init links the card layout routes (GET, PUT and DELETE /api/layout) into `sleipnir web`.
func init() {
	webRoutePackages = append(webRoutePackages, webRoutePackage{name: "layout", order: 50, register: func(srv *web.Server, _ seam.Host, env webRouteEnv) {
		layout.Register(srv, layout.Options{Home: env.Home})
	}})
}
