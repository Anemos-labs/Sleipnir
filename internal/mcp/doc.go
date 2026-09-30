// Package mcp is Sleipnir's Model Context Protocol client. It connects to
// external tool servers (local processes over stdio, remote servers over
// streamable HTTP or the legacy SSE transport) and turns their tools, prompts and
// resources into things the harness can use, without letting a server change
// what every agent sends to the model.
//
// # Shape
//
//	config      ServerConfig, Parse / ParseWith: both Sleipnir's {"name": {...}}
//	            and Claude Code's {"mcpServers": {...}} shapes; ${VAR} expansion
//	Manager     connects to all servers concurrently, supervises them (restart
//	            with backoff), hands out Snapshots, runs approval
//	Snapshot    the frozen, sorted, size-budgeted tool list plus its hash; its
//	            Tools() are ordinary tools.Tool values named mcp__<server>__<tool>
//	Client      one connection: JSON-RPC 2.0 correlation, the initialize
//	            handshake, tools/resources/prompts calls, cancellation, progress
//	Transport   newline-delimited stdio (a child process in its own process
//	            group), streamable HTTP (SSE responses, Mcp-Session-Id, a GET
//	            stream for server-initiated messages), legacy HTTP+SSE
//
// Clients are strict about what they send (exact JSON-RPC, exactly one offered
// protocol version, no capability they do not honour) and tolerant about what
// they receive (unknown fields and notifications, batches, numeric-string ids,
// banners on stdout, later protocol revisions), and never let the server
// choose how much they buffer: messages, listings, results, queues and
// failure counts are all bounded.
//
// # The tool list is frozen
//
// Every agent of a session sends the same tools array, byte for byte, so the
// provider caches it once for the whole swarm; a change to it rewrites the
// cached prefix of every agent. So the model-visible part of a server's tools is
// never live. Manager.Snapshot takes an immutable snapshot (names sorted,
// descriptions sanitised and capped with a marker, schemas canonical JSON, the
// whole under a byte budget shared fairly between servers, oversized or
// suspicious tools excluded with a warning) and a hash of it; the session freezes
// that. Servers keep changing: tools/list_changed, a restart into a newer
// version. The manager follows them and tells the owner (Options.OnChange), but
// only when what the model would see actually differs from the last adopted
// snapshot, because that is what an epoch costs. A crash and restart into the
// same tools is not an event. A tool of an old snapshot that its server no
// longer offers fails with a model-visible error; it never runs something else.
//
// # Security posture
//
// Everything a server sends is untrusted data.
//
// Text. Descriptions, schemas (every string in them), prompt and resource
// metadata, results and error messages are stripped of control characters and
// terminal escape sequences, of bidi controls, zero-width characters, variation
// selectors and Unicode tag characters (which a model reads as ASCII and a
// reviewer cannot see), and of invalid UTF-8, at the point they are decoded;
// lengths are capped. A narrow tripwire (see inject.go) excludes tools whose
// description or schema strings carry the crudest forms of instruction
// injection; it is a tripwire, not a guarantee. Images and audio are stored in
// the blob store and the model sees a placeholder with type and size, unless the
// harness opts in with Options.AttachMedia.
//
// Approval. A project's MCP configuration arrives with a repository and is
// untrusted: it can name any command, any URL, any header. Entries parsed with
// ParseOptions{Scope: ScopeUser} are trusted; everything else (project scope, or
// no scope given) is refused unless the caller marks it Trust in code or
// Options.Approve says yes, and a project file cannot vouch for itself (its
// "trust" and "allow_private" are cleared with a warning). This covers remote
// servers too, since their descriptions land in every agent's prompt. The hook
// receives the entry as written, with ${VAR} unexpanded, and ServerConfig.EnvRefs
// lists which variables it asks for; ServerConfig.Fingerprint identifies it for
// remembered approvals. Approval questions are asked one at a time.
//
// Environment. ${VAR} expands only from Options.Env, an explicit map (EnvMap
// converts os.Environ() when a caller wants that on purpose). A stdio server
// inherits SafeBaseEnv (PATH, HOME, locale, temp dir, certificate paths) plus
// what its own env block lists: none of the harness's API keys or tokens. Its
// command is found on the PATH it will run with, ignoring relative entries, so a
// planted executable in the repository cannot win a lookup. It leads its own
// session and process group, and shutdown is stdin EOF, then SIGTERM, then
// SIGKILL to the whole group (taskkill /T elsewhere).
//
// Network. HTTP transports connect through an address guard: the name is
// resolved by the transport, every address is classified, and the connection is
// made to a vetted IP literal (no DNS rebinding). Public addresses are allowed;
// private and loopback ones need allow_private; link-local (cloud metadata),
// multicast, reserved and the metadata addresses of the big clouds never. Plain
// http:// needs allow_private too. Redirects are followed only within the origin
// of the request (a custom header would otherwise travel to another host), the
// legacy SSE message endpoint must be on the origin of the stream, proxies are
// explicit (Options.Proxy), and Options.Dial replaces the guard with the
// caller's own.
//
// Secrets. Header and env values (and URLs, which may carry tokens in their
// path) never appear in errors, logs, status text or String output of a
// configuration, and are masked in stderr excerpts and in tool results, because
// a server that echoes its credentials back is a leak to the model.
//
// Permissions. Every MCP tool call asks first: a perm.Request naming the tool,
// a one-line summary, the arguments, Network for remote transports, Risk medium,
// and Writes unless the server's own readOnlyHint says otherwise (an untrusted
// hint that can only make the question less alarming). A missing permission
// checker is a refusal, not a pass. Sampling and elicitation requests from
// servers are refused; roots are offered to local servers only.
//
// Resources. One message is at most 16 MiB (larger ones stream past a skimmer
// that recovers the call's id, so that call fails at once); listings are capped
// in pages, items and bytes; a server's output flood cannot grow a queue; a
// connection that sends only garbage is cut off; results go through the
// standard truncation with a recall handle; a crash loop parks the server after
// a few failures instead of restarting it forever.
//
// # Not covered
//
// OAuth and other interactive authentication (pass a token in headers), MCP
// tasks, resumable SSE streams for POST responses, resource subscriptions, and
// the writes an MCP tool makes: they bypass Sleipnir's leases, staleness checks
// and checkpoints, so permission rules are the only barrier around them.
//
// # Example
//
// A CLI-free session that connects to a server, freezes its tools, registers
// them and runs one:
//
//	servers, err := mcp.ParseWith(cfg.MCP, mcp.ParseOptions{Scope: mcp.ScopeUser})
//	if err != nil { /* report: the valid servers are in the map anyway */ }
//
//	mgr := mcp.NewManager(mcp.Options{
//		Servers:  servers,
//		Env:      map[string]string{"GITHUB_TOKEN": token}, // all that ${VAR} may see
//		Cwd:      workspaceRoot,
//		Approve:  askTheUser,                                // for project-scoped entries
//		OnChange: func(c mcp.Change) { session.MarkEpoch() },
//	})
//	defer mgr.Close()
//	if err := mgr.Start(ctx); err != nil {
//		log.Print(err) // partial failure is normal: the others are serving
//	}
//
//	snap := mgr.Snapshot() // adopt: this is the tool list the session freezes
//	snap.Register(registry)
//	specs, _ := registry.Specs() // identical bytes for every agent
//
// See Example and the other examples for a runnable version.
package mcp
