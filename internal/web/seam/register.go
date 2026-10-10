// The route packages register their routes on a *web.Server and resolve tabs through a Host. B1's webHost constructs the host and
// calls these functions, in this order, after it has registered the stream route and /api/hello (the signatures are the contract;
// the packages are written by their owners):
//
//	wsvc.Register(srv *web.Server, h seam.Host)                              // internal/web/wsvc (B3): the Workspace routes
//	settings.Register(srv *web.Server, h seam.Host, o settings.Options)      // internal/web/settings (B4): settings, recorded sessions,
//	                                                                         // schedule, providers, doctor, update
//	runner.Register(srv *web.Server, h seam.Host, o runner.Options)          // internal/web/runner (B4): the command runner
//
// settings.Options carries Home, Version and Self (the user's home directory, the program version and the executable); runner.Options
// carries Self and Env (the executable and the environment of commands that call a provider). A route package never keeps a Tab or a
// session: it asks the host each time.
package seam
