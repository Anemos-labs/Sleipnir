// The route packages register their routes on a *web.Server and resolve tabs through a Host. The host in cmd/sleipnir constructs the
// Host and calls these functions, in this order, after it has registered the stream route and /api/hello (the signatures are the
// contract between the host and the packages):
//
//	wsvc.Register(srv *web.Server, h seam.Host)                              // internal/web/wsvc: the Workspace routes
//	settings.Register(srv *web.Server, h seam.Host, o settings.Options)      // internal/web/settings: the Settings pages
//	tools.Register(srv *web.Server, h seam.Host, o tools.Options)            // internal/web/tools: recorded sessions, schedule, doctor,
//	                                                                         // update
//	runner.Register(srv *web.Server, h seam.Host, o runner.Options)          // internal/web/runner: the command runner
//
// settings.Options carries Home, Version and Self (the user's home directory, the program version and the executable); tools.Options
// carries the same and Commit, Env and Cwd, and the functions that translate a recorded or followed log; runner.Options carries Self,
// Env and Cwd (the executable, the environment of commands that call a provider and the directory of a run that names no tab). A
// route package never keeps a Tab or a session: it asks the host each time.
package seam
