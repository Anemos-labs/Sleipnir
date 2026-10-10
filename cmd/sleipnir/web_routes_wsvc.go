package main

import (
	"github.com/anemos-labs/sleipnir/internal/web"
	"github.com/anemos-labs/sleipnir/internal/web/seam"
	"github.com/anemos-labs/sleipnir/internal/web/wsvc"
)

// init links the Workspace routes (CONTRACT.md 12, with the path completion and the permission check) into `sleipnir web`.
func init() {
	webRoutePackages = append(webRoutePackages, webRoutePackage{name: "wsvc", order: 10, register: func(srv *web.Server, h seam.Host, _ webRouteEnv) {
		wsvc.Register(srv, h)
	}})
}
