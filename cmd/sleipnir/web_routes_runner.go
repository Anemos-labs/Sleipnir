package main

import (
	"context"

	"github.com/anemos-labs/sleipnir/internal/web"
	"github.com/anemos-labs/sleipnir/internal/web/runner"
	"github.com/anemos-labs/sleipnir/internal/web/seam"
)

// init links the command runner into `sleipnir web`.
func init() {
	webRoutePackages = append(webRoutePackages, webRoutePackage{name: "runner", order: 40,
		register: func(srv *web.Server, h seam.Host, env webRouteEnv) {
			runner.Register(srv, h, runner.Options{Self: env.Self, Env: env.Env, Cwd: env.Cwd})
		},
		shutdown: func(ctx context.Context, srv *web.Server) { runner.Shutdown(ctx, srv) },
	})
}
