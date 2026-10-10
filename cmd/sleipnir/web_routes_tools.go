package main

import (
	"context"

	"github.com/anemos-labs/sleipnir/internal/web"
	"github.com/anemos-labs/sleipnir/internal/web/seam"
	webtools "github.com/anemos-labs/sleipnir/internal/web/tools"
)

// init links the recorded sessions, schedule, doctor and update routes (CONTRACT.md 7, 17, 19) into `sleipnir web`. Replaying and
// watching a recorded session need a translator of a log file; until one is given those two routes answer 501.
func init() {
	webRoutePackages = append(webRoutePackages, webRoutePackage{name: "tools", order: 30,
		register: func(srv *web.Server, h seam.Host, env webRouteEnv) {
			webtools.Register(srv, h, webtools.Options{Home: env.Home, Version: env.Version, Commit: commit, Self: env.Self, Env: env.Env, Cwd: env.Cwd})
		},
		shutdown: func(ctx context.Context, srv *web.Server) { webtools.Shutdown(ctx, srv) },
	})
}
