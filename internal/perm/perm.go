// Package perm decides whether an agent may perform an action.
//
// Tools describe what they are about to do as a Request (paths touched, shell
// command, whether it writes); the Engine applies the permission mode and
// rules, and asks a human (or a delegate) only when rules do not settle it.
//
// # Precedence
//
// A request is settled by the first of these that applies:
//
//  1. Hard denies: the built-in protections below, and user Deny rules. They win
//     over everything, bypass mode included.
//  2. User Ask rules: the answer is a question, whatever Allow rules or the mode say.
//  3. High-risk shell commands (see Shell commands): they ask unless a specific
//     Allow rule names them; plan mode denies them.
//  4. User Allow rules.
//  5. The mode's defaults.
//
// On top of that, a shell command the engine cannot see through is never
// auto-allowed by any rule (only bypass mode lets it pass), and a Deny always
// beats it: see Shell commands.
//
// Every Decision carries a short Reason naming the rule, protection or mode
// that decided it. Reasons are read by models, so they are deterministic and say
// what to do next.
//
// # Built-in protections
//
// Hard (no rule or mode lifts them): any access to ~/.ssh private keys (and
// writes to anything in ~/.ssh), ~/.aws, ~/.gnupg, ~/.config/gcloud (also as
// another user's home or a copy outside the workspace),
// /proc/*/environ, /etc/shadow and friends; writes to .git/** (change
// repositories with git commands, which are judged as commands and not as
// file writes); writes to /etc /usr /bin /sbin /boot /dev /lib /sys /proc
// (except harmless devices such as /dev/null); recursive delete, chmod or
// archive of a directory that contains any of those (rm -rf ~, rm -rf /,
// grep -r x ~). A workspace that itself sits under a system directory keeps
// working; a workspace of "/" does not switch the rule off. Protected names
// match case-insensitively, including the Unicode folds a case-insensitive
// file system applies (".SSH", ".\u017fsh").
//
// Guarded (denied unless an Allow rule names the path; a blanket Read or Read(**)
// does not count): **/.env and **/.env.* (except the templates .env.example,
// .env.sample, .env.template, .env.dist, .env.tpl and .env.defaults, which hold
// placeholders), ~/.ssh public keys, known_hosts and
// config (reads), and token files such as ~/.npmrc and ~/.git-credentials.
//
// # Workspace
//
// Config.Root (plus Config.ExtraRoots, for example per-agent worktrees) is the
// workspace. Relative paths, and relative patterns in rules, are relative to
// Root; a shell command runs in Request.Cwd, which defaults to Root.
//
// # Rules
//
// A rule is Tool or Tool(pattern), see Rule and ParseRule. Read(...) and
// Edit(...) rules are about access, not about which tool did it: they apply to
// the file tools and to the paths a shell command reads or writes alike.
// Path patterns are gitignore-like: "**" spans directories, "~" is the home
// directory, "./x" and "x/y" are relative to the workspace, an absolute path is
// itself, and a pattern with no slash matches at any depth (for Deny and Ask
// anywhere on disk, for Allow inside the workspace). A pattern covers the
// directory it names and everything below. Paths are compared after symlinks
// are followed, and ".." after a link is applied to the link's target; an
// Allow rule only ever matches the resolved path, so a link cannot borrow the
// permission of the place it imitates, while Deny and Ask rules look at both
// forms. Bash rules match the parsed command: "Bash(git status:*)" is a token
// prefix, "Bash(npm run test)" is exact, "Bash(*)" or "Bash" is any command.
// Allow rules match the command as written (./git is not git); Deny and Ask
// rules also see through sudo, env, nohup, time, absolute paths and quoting.
//
// # Modes
//
//   - ModeDefault: reads inside the workspace and read-only shell commands are
//     allowed; everything else asks.
//   - ModeAcceptEdits: also writes inside the workspace, including redirections
//     and mkdir, touch, cp, mv, rm, rmdir, tee on workspace paths.
//   - ModePlan: read-only. Anything that writes, reaches the network or is not a
//     provably read-only command is denied with a message that says so. Allow
//     rules still carve exceptions (say, Edit(docs/plan.md)).
//   - ModeBypass: allows everything except hard denies, Deny rules, guarded
//     paths without an Allow rule, and Ask rules. Meant for sandboxes.
//
// # Shell commands
//
// A command line is parsed with package shellparse. Every simple command in it,
// including those inside $(...), backticks, <(...), eval and sh -c strings, is
// judged; the whole is allowed only if every part is, and denied if any part is.
// The read-only allowlist covers ls, cat, head, tail, wc, pwd, echo, which,
// whoami, date, uname, stat, file, du, df, tree, sort, uniq, cut, tr, diff, cmp,
// basename, dirname, realpath, rg, grep, find, a few builtins, git (status, diff,
// log, show, branch without changes, remote -v, rev-parse, ls-files, blame, ...),
// go (version, env, list, vet, build, doc) and "--version" probes. Options that
// write or run programs (find -exec/-delete, sort -o, rg --pre, git -c,
// go build -toolexec, ...) take a command off the list. printenv and env are not
// on it: they dump secrets.
//
// Never auto-allowed (they ask, or are denied in plan mode) unless the mode is
// bypass: a command substitution or process substitution, input the parser marks
// unparsed (unterminated quotes, case, function definitions, ...), a pipe into a
// shell or interpreter (curl | sh), a command name built from a variable, an
// environment assignment that changes what runs (PATH, LD_PRELOAD, GIT_*),
// paths that cannot be resolved statically ($X), quoted text that looks like a
// command substitution where bash could evaluate it anyway (declare 'a[$(cmd)]',
// x='a[$(cmd)]'; echo $((x))), and a line with thousands of commands or
// operands. Inner commands are still checked, so echo $(cat ~/.ssh/id_rsa) is
// denied, not merely asked about, and so are the strings given to eval, sh -c,
// su -c and trap, the word list of a for loop, and heredocs fed to a shell.
//
// High risk (ask; plan mode denies; a blanket Bash(*) does not lift it): sudo and
// friends, rm -rf of the workspace, home, "/", "*" or a parent, recursive chmod
// 777, disk tools and dd to devices, force-pushing to main/master/shared
// branches, git reset --hard on a shared branch, shutdown. Only an Allow rule
// that is exact, or that names the risky flag, overrides the flag.
//
// Redirections count as file accesses: ">" writes its target, "<" reads it, and
// "2>&1" or "> /dev/null" touch nothing. Relative paths follow cd, judged
// against every directory the shell might be in. Globs are expanded against
// the real file system, so ~/.s*/id_rsa is ~/.ssh/id_rsa. For commands not on
// any list every operand (and option value, and the pieces of a:b lists) is
// treated as a path that may be read and written, recursively.
//
// Request.Writes and Request.Network describe non-shell tools; for a shell
// request the engine trusts its own analysis, not those flags.
//
// # Asking
//
// When the outcome is "ask", the Prompter is consulted. With none, the request
// is denied with reason "approval required: ...". Prompter calls are serialised
// per Engine, so a swarm never shows two questions at once, and identical
// requests (same tool, command, directory, paths, effects) that arrive while one
// is pending share a single answer. The Prompter is given the request with the
// reason for asking appended to Summary, the one line a human reads.
//
// A caller's context cancels only that caller; if the caller that was asking
// gives up, a waiting one asks again itself. The Prompter runs while the engine
// holds its prompt lock, so it must not trigger another question on the same
// Engine.
//
// Decision.Remember turns an answer into an exact rule (a command, a path, a
// host) for the session or, through Config.Persist, the project; a refusal is
// remembered as a Deny rule. Constructs that always ask (substitutions and the
// like) and questions caused by an Ask rule are never remembered.
//
// # Roles
//
// Request.Role selects a RoleProfile from Config.Roles. The request is judged
// under the session and under the profile's overlay, and the more severe outcome
// stands, so a profile can tighten (deny, ask, a stricter mode; a read-only
// reviewer is Mode: ModePlan) and never loosen. A role that takes a stricter
// mode than the session is judged from scratch under it: the session's Allow
// rules do not carve exceptions out of the role's mode, only the role's own
// Allow rules do (and only up to what the session itself permits).
//
// # Limits
//
// The check happens before the action, so a file swapped for a symlink in
// between is not seen (tools should open with O_NOFOLLOW where they can);
// hard links to protected files are not detected; shell aliases and functions
// defined outside the command are unknown; what a program does with an
// operand it is not known to treat as a file is not modelled beyond treating
// operands as paths; recursive searches that merely pass a .env file are not
// stopped (only naming one is); commands run on remote machines (ssh host cmd)
// are opaque and ask; and in bypass mode the constructs listed above are not
// analysed at all, so a Deny rule cannot see through, say, a command name
// built by a substitution.
package perm

import (
	"context"
	"encoding/json"
)

// Mode is the coarse posture of a session or agent.
type Mode string

const (
	// ModeDefault allows reads inside the workspace and asks for the rest.
	ModeDefault Mode = "default"
	// ModeAcceptEdits also allows file edits inside the workspace.
	ModeAcceptEdits Mode = "accept-edits"
	// ModePlan is read-only: anything that writes or executes is denied.
	ModePlan Mode = "plan"
	// ModeBypass allows everything except hard denies. For sandboxes only.
	ModeBypass Mode = "bypass"
)

// Risk is a tool's own estimate of how dangerous an action is.
type Risk int

const (
	RiskLow Risk = iota
	RiskMedium
	RiskHigh
)

// Request describes one action awaiting a decision.
type Request struct {
	Agent string          `json:"agent"`
	Role  string          `json:"role,omitempty"`
	Tool  string          `json:"tool"`
	Input json.RawMessage `json:"input,omitempty"`

	// Filled in by the tool so matching never re-parses Input.
	Summary string   `json:"summary"`           // one line for humans
	Paths   []string `json:"paths,omitempty"`   // absolute paths touched
	Command string   `json:"command,omitempty"` // shell command line
	Cwd     string   `json:"cwd,omitempty"`     // directory the command runs in; defaults to the engine's Root
	Writes  bool     `json:"writes,omitempty"`  // mutates workspace or system
	Network bool     `json:"network,omitempty"` // reaches the network
	Risk    Risk     `json:"risk,omitempty"`
}

// Scope says how long a remembered decision lasts.
type Scope string

const (
	ScopeOnce    Scope = ""
	ScopeSession Scope = "session"
	ScopeProject Scope = "project"
)

// Decision is the outcome of a check.
type Decision struct {
	Allow  bool   `json:"allow"`
	Reason string `json:"reason,omitempty"`
	// Remember asks the engine to persist this answer as a rule.
	Remember Scope `json:"remember,omitempty"`
}

// Requester is what tools call before acting.
type Requester interface {
	Check(ctx context.Context, r Request) Decision
}

// Prompter asks a human (or a delegating agent) to decide.
type Prompter func(ctx context.Context, r Request) Decision

// AllowAll is a Requester that permits everything (tests, bypass sandboxes).
type AllowAll struct{}

// Check implements Requester.
func (AllowAll) Check(context.Context, Request) Decision {
	return Decision{Allow: true, Reason: "allow-all"}
}
