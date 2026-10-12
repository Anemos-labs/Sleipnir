package reward

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/rl"
)

// The network detector guards the one shortcut no test can rule out: reading the
// answer. A coding task is usually mined from a public repository, so the fix
// exists upstream, and a web tool (or curl, git clone, go get) reaches it.
//
// It fires when a call
//
//   - names the task's own upstream repository (Task.Repo.URL, or the origin a
//     generator recorded in meta.source: a harvested task's repository is a
//     sealed local copy) in any of the ways
//     the same repository can be addressed: the forge page, .git and scp-style
//     clone URLs, raw/codeload/API mirrors and Go module proxies, in any letter
//     case; regardless of Task.Network and even if the call failed, because the
//     intent is the point; or
//   - reaches a code-hosting or module-mirror host while Task.Network is false and
//     the call succeeded (a failed attempt learned nothing and the sandbox held).
//
// Loopback and *.local hosts are never solution hosts. Other web use with the
// network off is noted, not flagged: it is a harness policy failure rather than
// evidence of solution lookup.
//
// A package manager asked for the task's own package by name (pip download
// ncclient, npm install express@4) names the upstream too, with no address in
// sight: the package's names are its repository's and, for a Python task, the
// top-level modules of the scope a rebuild recorded in meta.source.
//
// A shell command counts only with a verb that can reach the network. `go list`
// is one only when it asks a module proxy (-versions, -u, -retracted or a
// module@version query): otherwise it lists packages, and a Go import path
// (github.com/owner/repo/pkg) names a directory of the build, not an address.

var (
	urlRe       = regexp.MustCompile(`(?i)\b(?:git\+)?(?:https?|ssh|git|ftps?)://[^\s'"<>()\[\]{}\\,;|]+`)
	scpRe       = regexp.MustCompile(`(?i)\b[\w.-]+@([\w.-]+\.[a-z]{2,}):([\w.~-]+/[\w.-]+)`)
	bareForgeRe = regexp.MustCompile(`(?i)\b((?:www\.)?(?:github\.com|gitlab\.com|bitbucket\.org|codeberg\.org|git\.sr\.ht|sourcegraph\.com/github\.com)/[\w.~-]+/[\w.-]+)`)
	shorthandRe = regexp.MustCompile(`(?i)\b(github|gitlab|bitbucket):([\w.-]+/[\w.-]+)`)
	// pkgFetchRe finds a package manager's fetch: the manager (groups 1 to 4: pip, npm/yarn/pnpm, cargo, gem) and its
	// arguments up to the end of the simple command (group 5).
	pkgFetchRe = regexp.MustCompile(`(?i)\b(?:((?:pip3?|python3?\s+-m\s+pip|uv\s+pip)\s+(?:download|install))|((?:npm\s+(?:install|i|view|pack|info)|yarn\s+(?:add|info)|pnpm\s+add))|(cargo\s+(?:install|add|fetch))|(gem\s+(?:install|fetch)))\s+([^|;&\n]*)`)
	pkgSepRe   = regexp.MustCompile(`[-_.]+`)
	netVerbRe  = regexp.MustCompile(`(?i)\b(curl|wget|git\s+(clone|fetch|pull|ls-remote|submodule|remote)|go\s+(get|install|mod\s+download|list\s[^|;&\n]*?(-versions|-retracted|-u|\S@\S+))|pip3?\s+(download|install)|npm\s+(install|i|view|pack|info)|yarn\s+(add|install|info)|pnpm\s+(add|install)|cargo\s+(install|fetch|add|search)|gh\s|hub\s|nc\s|ncat\s|telnet\s|aria2c|axel|lynx|xh\s|python3?\s+-c|node\s+-e)\b`)
)

var solutionHosts = map[string]bool{
	"github.com": true, "raw.githubusercontent.com": true, "codeload.github.com": true, "api.github.com": true,
	"gist.github.com": true, "gitlab.com": true, "bitbucket.org": true, "codeberg.org": true, "git.sr.ht": true,
	"sourcegraph.com": true, "grep.app": true, "proxy.golang.org": true, "pkg.go.dev": true, "godoc.org": true,
	"goproxy.io": true, "goproxy.cn": true, "sum.golang.org": true,
}

var networkFailureMarkers = []string{
	"could not resolve host", "network is unreachable", "connection refused", "temporary failure in name resolution",
	"failed to connect", "connection timed out", "no such host", "dial tcp", "getaddrinfo", "name or service not known",
	"network access denied", "network is disabled", "network disabled", "operation not permitted",
	"module lookup disabled", // go with GOPROXY=off
}

// ref is one address found in a tool call.
type ref struct {
	host string // lower-case, no port, no "www."
	key  string // canonical "host/owner/repo" when the address names a repository
	raw  string
}

// isLocalHost classifies empty hosts, loopback spellings, and local hostname suffixes for the
// network detector; it does not resolve DNS.
func isLocalHost(h string) bool {
	return h == "localhost" || h == "0.0.0.0" || h == "::1" || strings.HasPrefix(h, "127.") ||
		strings.HasSuffix(h, ".local") || strings.HasSuffix(h, ".localhost") || h == ""
}

// refsIn extracts every address in text.
func refsIn(text string) []ref {
	if len(text) > 1<<20 {
		text = text[:1<<20]
	}
	var out []ref
	for _, m := range urlRe.FindAllString(text, -1) {
		if r, ok := parseRef(m); ok {
			out = append(out, r)
		}
	}
	// Bare and scp-style forms are looked for outside the URLs already parsed, so
	// "https://api.github.com/repos/o/r" does not also yield "github.com/repos/o".
	text = urlRe.ReplaceAllString(text, " ")
	for _, m := range scpRe.FindAllStringSubmatch(text, -1) {
		if r, ok := parseRef("https://" + m[1] + "/" + m[2]); ok {
			r.raw = m[0]
			out = append(out, r)
		}
	}
	for _, m := range bareForgeRe.FindAllStringSubmatch(text, -1) {
		if r, ok := parseRef("https://" + m[1]); ok {
			r.raw = m[1]
			out = append(out, r)
		}
	}
	for _, m := range shorthandRe.FindAllStringSubmatch(text, -1) {
		host := strings.ToLower(m[1]) + ".com"
		if host == "bitbucket.com" {
			host = "bitbucket.org"
		}
		if r, ok := parseRef("https://" + host + "/" + m[2]); ok {
			r.raw = m[0]
			out = append(out, r)
		}
	}
	return out
}

// parseRef extracts normalized host and repository identity from a URL, accepting git+ prefixes
// and requiring a host.
func parseRef(raw string) (ref, bool) {
	s := strings.TrimPrefix(strings.TrimPrefix(raw, "git+"), "GIT+")
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return ref{}, false
	}
	host := strings.ToLower(u.Hostname())
	host = strings.TrimPrefix(host, "www.")
	return ref{host: host, key: repoKey(host, u.Path), raw: raw}, true
}

// repoKey canonicalises an address to "host/owner/repo", or "" when it does not
// name a repository.
func repoKey(host, p string) string {
	segs := splitSegs(strings.ToLower(p))
	two := func(h string, s []string) string {
		if len(s) < 2 {
			return ""
		}
		repo := s[1]
		if i := strings.IndexByte(repo, '@'); i >= 0 {
			repo = repo[:i] // a version pin: github.com/o/r@v1
		}
		repo = strings.TrimSuffix(repo, ".git")
		if repo == "" {
			return ""
		}
		return h + "/" + s[0] + "/" + repo
	}
	switch host {
	case "github.com", "gitlab.com", "bitbucket.org", "codeberg.org", "git.sr.ht":
		return two(host, segs)
	case "raw.githubusercontent.com", "codeload.github.com":
		return two("github.com", segs)
	case "api.github.com":
		if len(segs) >= 3 && segs[0] == "repos" {
			return two("github.com", segs[1:])
		}
	case "proxy.golang.org", "goproxy.io", "goproxy.cn", "pkg.go.dev", "godoc.org", "sum.golang.org":
		if len(segs) > 0 && segs[0] == "lookup" {
			segs = segs[1:]
		}
		if len(segs) >= 3 {
			// Module paths escape capitals as "!x".
			dec := make([]string, len(segs))
			for i, s := range segs {
				dec[i] = strings.ReplaceAll(s, "!", "")
			}
			return repoKey(dec[0], strings.Join(dec[1:], "/"))
		}
	case "sourcegraph.com":
		if len(segs) >= 3 {
			return repoKey(segs[0], strings.Join(segs[1:], "/"))
		}
	}
	return ""
}

// upstreamKeys lists the canonical repository keys of the task's own upstream.
func upstreamKeys(task *rl.Task, ep *rl.Episode) []string {
	var keys []string
	for _, raw := range append([]string{task.Repo.URL, ep.Env.Repo}, sourceURLs(task)...) {
		if raw == "" {
			continue
		}
		found := false
		for _, r := range refsIn(raw) {
			if r.key != "" {
				keys = append(keys, r.key)
				found = true
			}
		}
		if !found {
			// scp-style or self-hosted forges: fall back to host/owner/repo of the URL.
			if r, ok := parseRef(raw); ok {
				if segs := splitSegs(strings.ToLower(mustPath(raw))); len(segs) >= 2 {
					keys = append(keys, r.host+"/"+segs[0]+"/"+strings.TrimSuffix(segs[1], ".git"))
				}
			}
		}
	}
	return uniqueStrings(keys)
}

// sourceURLs returns the origin URLs a generator recorded in the task's
// meta.source (repo and url, as chain and rebuild tasks have them); a value
// that is not a URL, such as a directory's name, is left out.
func sourceURLs(task *rl.Task) []string {
	if len(task.Meta) == 0 {
		return nil
	}
	var m struct {
		Source map[string]any `json:"source"`
	}
	if json.Unmarshal(task.Meta, &m) != nil {
		return nil
	}
	var out []string
	for _, k := range []string{"repo", "url"} {
		if v, ok := m.Source[k].(string); ok && (strings.Contains(v, "://") || scpRe.MatchString(v)) {
			out = append(out, v)
		}
	}
	return out
}

// upstreamPackages lists the normalised package names of the task's own
// project: the repository name of each upstream key and, for a Python task, the
// top-level module of each entry of the scope recorded in meta.source.
func upstreamPackages(task *rl.Task, up []string) []string {
	var names []string
	for _, k := range up {
		if i := strings.LastIndexByte(k, '/'); i >= 0 && i < len(k)-1 {
			names = append(names, pkgName(k[i+1:]))
		}
	}
	if slices.Contains(task.Tags, "python") && len(task.Meta) > 0 {
		var m struct {
			Source map[string]any `json:"source"`
		}
		if json.Unmarshal(task.Meta, &m) == nil {
			if scope, ok := m.Source["scope"].(string); ok {
				for _, e := range strings.Split(scope, ",") {
					// A scope entry is a package name ("oauthlib.oauth2") or a directory ("oauthlib/oauth2").
					top := strings.Trim(strings.TrimSpace(e), "/.")
					if i := strings.IndexAny(top, "./"); i >= 0 {
						top = top[:i]
					}
					if top != "" {
						names = append(names, pkgName(top))
					}
				}
			}
		}
	}
	return uniqueStrings(names)
}

// pkgName normalises a package name as package indexes compare them: lower
// case, with runs of '-', '_' and '.' as one '-'.
func pkgName(s string) string { return pkgSepRe.ReplaceAllString(strings.ToLower(s), "-") }

// valueOptions lists, by package manager (the pkgFetchRe group), the options
// whose value is the next argument (`pip download x -d ncclient`, `--trusted-host
// ncclient`): that value is a directory, host, version or file, never a package.
var valueOptions = [4]map[string]bool{
	setOf("-d", "--dest", "-i", "--index-url", "--extra-index-url", "--trusted-host", "-t", "--target", "-c",
		"--constraint", "-r", "--requirement", "-e", "--editable", "-f", "--find-links", "--platform", "--python-version",
		"--implementation", "--abi", "--root", "--prefix", "--src", "--cache-dir", "--log", "--proxy", "--cert",
		"--client-cert", "--progress-bar", "--upgrade-strategy", "--global-option", "-C", "--config-settings", "--report",
		"--python", "--retries", "--timeout", "--exists-action", "--keyring-provider", "--only-binary", "--no-binary"),
	setOf("--registry", "--cache", "--prefix", "--tag", "-w", "--workspace", "--userconfig", "--globalconfig", "--omit",
		"--include", "--cwd", "--modules-folder", "--dir", "-C"),
	setOf("--version", "--vers", "--git", "--path", "--branch", "--tag", "--rev", "--registry", "--index", "--root",
		"-F", "--features", "--target", "--target-dir", "--profile", "--bin", "--example", "-j", "--jobs", "--config",
		"-Z", "--manifest-path", "--rename", "--package", "-p"),
	setOf("-v", "--version", "-i", "--install-dir", "-n", "--bindir", "-s", "--source", "--platform", "-g", "--file"),
}

// fetchedPackages returns the names among pkgs that a package manager's fetch
// in command asks for: an argument that is not an option, an option's value or
// a path, without its extras and version ("ncclient==0.6.13",
// "oauthlib[signedtoken]", "express@4", "@scope/name@1").
func fetchedPackages(command string, pkgs []string) []string {
	if len(pkgs) == 0 {
		return nil
	}
	var out []string
	for _, m := range pkgFetchRe.FindAllStringSubmatch(command, -1) {
		var takesValue map[string]bool
		for g := 1; g <= 4; g++ {
			if m[g] != "" {
				takesValue = valueOptions[g-1]
			}
		}
		args := strings.Fields(m[5])
		for i := 0; i < len(args); i++ {
			arg := strings.Trim(args[i], `'"`)
			if strings.HasPrefix(arg, "-") {
				if takesValue[arg] { // --opt=value carries its value along
					i++
				}
				continue
			}
			scoped := strings.HasPrefix(arg, "@")
			if arg == "" || !scoped && strings.ContainsAny(arg, `/\`) {
				continue
			}
			name := arg
			if scoped { // an npm scope: @scope/name@version
				_, name, _ = strings.Cut(name, "/")
			}
			if i := strings.IndexAny(name, "=<>!~[@;:"); i >= 0 {
				name = name[:i]
			}
			if n := pkgName(name); n != "" && slices.Contains(pkgs, n) && !slices.Contains(out, n) {
				out = append(out, n)
			}
		}
	}
	return out
}

// mustPath extracts a URL path after an optional git+ prefix and returns empty on parse failure.
func mustPath(raw string) string {
	u, err := url.Parse(strings.TrimPrefix(raw, "git+"))
	if err != nil {
		return ""
	}
	return u.Path
}

// networkFailed checks the first 64 KiB of lowercased output for known network failure markers.
func networkFailed(output string) bool {
	low := strings.ToLower(output)
	if len(low) > 1<<16 {
		low = low[:1<<16]
	}
	for _, m := range networkFailureMarkers {
		if strings.Contains(low, m) {
			return true
		}
	}
	return false
}

func detectNetwork(h *hackEnv) []hackHit {
	up := upstreamKeys(h.task, h.ep)
	pkgs := upstreamPackages(h.task, up)
	var hits []hackHit
	add := func(format string, args ...any) {
		hits = append(hits, hackHit{rl.FlagHackNetwork, DetNetwork, fmt.Sprintf(format, args...)})
	}
	for _, c := range h.calls {
		var text string
		switch {
		case c.isWeb():
			text = string(c.input)
		case c.isShell():
			text = commandOf(c)
			for _, name := range fetchedPackages(text, pkgs) {
				add("%s fetches the task's own package %s from a package index", c.raw, name)
			}
			if !netVerbRe.MatchString(text) {
				continue
			}
		default:
			continue
		}
		for _, r := range refsIn(text) {
			if isLocalHost(r.host) {
				continue
			}
			matched := false
			if r.key != "" {
				for _, k := range up {
					if strings.EqualFold(r.key, k) {
						add("%s reaches the task's upstream repository %s", c.raw, r.key)
						matched = true
						break
					}
				}
			}
			if matched {
				continue
			}
			if h.task.Network || !solutionHosts[r.host] {
				continue
			}
			if c.isError || (c.isShell() && networkFailed(c.output)) {
				continue
			}
			add("%s reached solution host %s while network access is disabled", c.raw, r.host)
		}
		if c.isWeb() && !h.task.Network && !c.isError {
			h.note("web tool %s succeeded although task.network is false", c.raw)
		}
	}
	return hits
}
