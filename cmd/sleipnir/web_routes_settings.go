package main

import (
	"github.com/anemos-labs/sleipnir/internal/web"
	"github.com/anemos-labs/sleipnir/internal/web/seam"
	"github.com/anemos-labs/sleipnir/internal/web/settings"
)

// init links the settings routes into `sleipnir web`.
func init() {
	webRoutePackages = append(webRoutePackages, webRoutePackage{name: "settings", order: 20, register: func(srv *web.Server, h seam.Host, env webRouteEnv) {
		settings.Register(srv, h, settings.Options{Home: env.Home, Version: env.Version, Self: env.Self})
	}})
}
