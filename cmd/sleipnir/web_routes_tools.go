package main

import (
	"context"
	"encoding/json"

	"github.com/anemos-labs/sleipnir/internal/web"
	"github.com/anemos-labs/sleipnir/internal/web/seam"
	webtools "github.com/anemos-labs/sleipnir/internal/web/tools"
	"github.com/anemos-labs/sleipnir/internal/web/translate"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// init links the recorded sessions, schedule, doctor and update routes (CONTRACT.md 7, 17, 19) into `sleipnir web`. A recorded
// session is replayed and watched through the event translator: its log is read with every row from the log, and its text is cleaned
// and masked as a live session's; the project root is the one the log's session.start names.
func init() {
	webRoutePackages = append(webRoutePackages, webRoutePackage{name: "tools", order: 30,
		register: func(srv *web.Server, h seam.Host, env webRouteEnv) {
			webtools.Register(srv, h, webtools.Options{Home: env.Home, Version: env.Version, Commit: commit, Self: env.Self, Env: env.Env, Cwd: env.Cwd,
				Replay: replayRecorded, Follow: followRecorded})
		},
		shutdown: func(ctx context.Context, srv *web.Server) { webtools.Shutdown(ctx, srv) },
	})
}

// replayRecorded translates a recorded session's log into the page's UI events (GET /api/recorded/{sid}/events, FEATURES.md D-10).
func replayRecorded(ctx context.Context, dir string) ([]json.RawMessage, error) {
	return translate.Replay(ctx, dir, translate.Config{Tab: "recorded"})
}

// followRecorded publishes the UI events of a log another process writes for the read-only tab tab, until ctx ends or the session
// ends (PARITY.md A7).
func followRecorded(ctx context.Context, tab, dir string, publish func(wire.Frame)) error {
	return translate.FollowDir(ctx, translate.Config{Tab: tab, Publish: publish}, dir)
}
