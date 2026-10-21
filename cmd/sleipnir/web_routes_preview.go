package main

import (
	"github.com/anemos-labs/sleipnir/internal/web"
	"github.com/anemos-labs/sleipnir/internal/web/preview"
	"github.com/anemos-labs/sleipnir/internal/web/seam"
)

// init links the diagram frame of the file previews (GET /api/preview/mermaid) into `sleipnir web`.
func init() {
	webRoutePackages = append(webRoutePackages, webRoutePackage{name: "preview", order: 60, register: func(srv *web.Server, _ seam.Host, _ webRouteEnv) {
		preview.Register(srv)
	}})
}
