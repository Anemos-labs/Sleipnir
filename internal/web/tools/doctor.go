package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/provider/probe"
	"github.com/anemos-labs/sleipnir/internal/session"
	"github.com/anemos-labs/sleipnir/internal/update"
	"github.com/anemos-labs/sleipnir/internal/web"
	"github.com/anemos-labs/sleipnir/internal/web/runner"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// The Doctor (CONTRACT.md 19): a probe of an endpoint is `sleipnir doctor --json` run by the runner, with the held keys (it sends a
// few billable requests, as the command does). Its progress lines on standard error become step frames ({step: DoctorStep}, in the
// groups the probe announces), its report on standard output becomes the verdict of the last frame ({verdict, result}): what the
// endpoint does, its warnings and a summary card. Running the command keeps the doctor of the page and of the terminal one thing
// (the same endpoint resolution, the same key handling, the same report).

// registerDoctor adds the doctor and update routes.
func (s *service) registerDoctor() {
	s.srv.HandleFunc("GET /api/doctor/endpoints", s.handleEndpoints, web.RouteOpts{})
	s.srv.HandleFunc("POST /api/doctor", s.handleDoctor, web.RouteOpts{})
	s.srv.HandleFunc("GET /api/update", s.handleUpdate, web.RouteOpts{WriteTimeout: 45 * time.Second})
	s.srv.HandleFunc("POST /api/update/install", s.handleInstall, web.RouteOpts{NoBody: true, WriteTimeout: 45 * time.Second})
}

// handleEndpoints is GET /api/doctor/endpoints: the models the configuration names (the default, the roles', the favourites), each
// with where it is served and which variable holds its key (never the key).
func (s *service) handleEndpoints(w http.ResponseWriter, r *http.Request) {
	cfg, _, err := config.Load(config.LoadOpts{Cwd: s.o.Cwd, Home: s.o.Home, UntrustedProject: true})
	out := []wire.DoctorEndpoint{}
	if err != nil || cfg == nil {
		web.Logf(r, "the configuration: %v", err)
		_ = web.WriteJSON(w, http.StatusOK, map[string]any{"endpoints": out})
		return
	}
	seen := map[string]bool{}
	addRef := func(ref string) {
		mr, err := session.ResolveModel(cfg, ref)
		if err != nil || seen[mr.String()] {
			return
		}
		seen[mr.String()] = true
		where := mr.Provider
		if base, keyEnv, ok := session.ProviderInfo(cfg, mr.Provider); ok {
			key := "no key"
			if keyEnv != "" {
				key = "key from $" + keyEnv
			}
			where = fmt.Sprintf("%s (%s, %s)", mr.Provider, base, key)
		}
		out = append(out, wire.DoctorEndpoint{Ref: mr.String(), Where: runner.Clean(where)})
	}
	addRef("")
	roles := make([]string, 0, len(cfg.Models.Roles))
	for role := range cfg.Models.Roles {
		roles = append(roles, role)
	}
	sort.Strings(roles)
	for _, role := range roles {
		addRef(cfg.Models.Roles[role])
	}
	for _, f := range cfg.Models.Favorites {
		addRef(f)
	}
	_ = web.WriteJSON(w, http.StatusOK, map[string]any{"endpoints": out})
}

// doctorRequest is the body of POST /api/doctor.
type doctorRequest struct {
	Model   string `json:"model,omitempty"`
	BaseURL string `json:"baseUrl,omitempty"`
	Deep    bool   `json:"deep,omitempty"`
}

// modelRefRE is what a model reference may be: provider/model or a bare id, no white space, no leading dash.
var modelRefRE = regexp.MustCompile(`^[A-Za-z0-9_.:@][A-Za-z0-9_.:@/+-]{0,199}$`)

// handleDoctor is POST /api/doctor {model?, baseUrl?, deep}: start a probe; 202 RunStarted, then run frames.
func (s *service) handleDoctor(w http.ResponseWriter, r *http.Request) {
	var body doctorRequest
	if !web.DecodeJSON(w, r, &body) {
		return
	}
	body.Model, body.BaseURL = strings.TrimSpace(body.Model), strings.TrimSpace(body.BaseURL)
	if body.BaseURL != "" && body.Model == "" {
		web.Error(w, http.StatusBadRequest, "bad_flags", "a custom endpoint needs --base-url and --model")
		return
	}
	if body.Model != "" && !modelRefRE.MatchString(body.Model) {
		web.Error(w, http.StatusBadRequest, "bad_flags", "--model is written provider/model")
		return
	}
	if body.BaseURL != "" {
		u, err := url.Parse(body.BaseURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || len(body.BaseURL) > 2048 || u.User != nil {
			web.Error(w, http.StatusBadRequest, "bad_flags", "--base-url is an http or https URL")
			return
		}
	}
	args := []string{"doctor", "--json"}
	flags := map[string]any{"json": true}
	if body.Model != "" {
		args = append(args, "--model="+body.Model)
		flags["model"] = body.Model
	}
	if body.BaseURL != "" {
		args = append(args, "--base-url="+body.BaseURL)
		flags["base-url"] = body.BaseURL
	}
	if body.Deep {
		args = append(args, "--deep")
		flags["deep"] = true
	}
	p := &doctorParse{}
	started, err := s.runs.Start(runner.Spec{
		// a custom endpoint is probed without the held keys: a key goes only where the configuration sends it
		Path: []string{"doctor"}, Flags: flags, Args: args, Cmdline: runner.Cmdline(args), Net: body.BaseURL == "",
		Line: p.line, End: p.end,
	})
	if err != nil {
		web.WriteError(w, err)
		return
	}
	web.Logf(r, "doctor started as %s", started.ID)
	_ = web.WriteJSON(w, http.StatusAccepted, started)
}

// doctorParse reads a probe's output: the progress lines on standard error (a group name, then "  ✓ name  123ms  in=… cached=…
// out=…" or "  ✗ name  error" per request, "    ! warning"), and the JSON report on standard output.
type doctorParse struct {
	mu    sync.Mutex
	group string
	warn  []string
	out   strings.Builder
	errs  []string
}

var (
	stepRE  = regexp.MustCompile(`^\s+([✓✗])\s+(\S+)\s*(?:(\d+)ms)?\s*(.*)$`)
	countRE = regexp.MustCompile(`(\w+)=(\d+)`)
)

// maxReport bounds the report read from standard output.
const maxReport = 1 << 20

// line takes one line of the probe's output.
func (p *doctorParse) line(stream, text string) (*wire.DoctorStep, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if stream == "out" {
		if p.out.Len()+len(text) < maxReport {
			p.out.WriteString(text)
			p.out.WriteByte('\n')
		}
		return nil, false
	}
	if m := stepRE.FindStringSubmatch(text); m != nil {
		st := &wire.DoctorStep{OK: m[1] == "✓", Name: m[2], Grp: p.group}
		st.Ms, _ = strconv.ParseInt(m[3], 10, 64)
		for _, kv := range countRE.FindAllStringSubmatch(m[4], -1) {
			n, _ := strconv.Atoi(kv[2])
			switch kv[1] {
			case "in":
				st.In = n
			case "cached":
				st.Cached = n
			case "out":
				st.Out = n
			}
		}
		if !st.OK {
			if d := strings.TrimSpace(m[4]); d != "" {
				p.errs = append(p.errs, st.Name+": "+d)
			}
		}
		return st, false
	}
	trimmed := strings.TrimSpace(text)
	switch {
	case strings.HasPrefix(trimmed, "! "):
		p.warn = append(p.warn, strings.TrimPrefix(trimmed, "! "))
		return nil, false
	case text != "" && text[0] != ' ' && !strings.Contains(text, " ") && !strings.HasPrefix(text, "sleipnir"):
		p.group = trimmed // a group the probe announces: basic, tools, cache, reasoning, min-prefix, warm-up
		return nil, false
	}
	return nil, trimmed != ""
}

// end turns the report into the verdict of the last frame.
func (p *doctorParse) end(res *wire.RunResult) *wire.DoctorVerdict {
	p.mu.Lock()
	defer p.mu.Unlock()
	var rep probe.Report
	if err := json.Unmarshal([]byte(p.out.String()), &rep); err != nil || len(rep.Steps) == 0 {
		if len(p.warn) == 0 && len(p.errs) == 0 {
			return nil
		}
		return &wire.DoctorVerdict{Warn: append(append([]string{}, p.warn...), p.errs...)}
	}
	v := Verdict(&rep)
	v.Warn = append(append([]string{}, p.warn...), v.Warn...)
	res.Card = v.Card
	return v
}

// Verdict is what an endpoint does, as the probe's report says it (the lines of probe.Report.Text, as label and value), its notes,
// and a summary card.
func Verdict(rep *probe.Report) *wire.DoctorVerdict {
	f := rep.Findings
	yn := func(b bool) string {
		if b {
			return "yes"
		}
		return "NO"
	}
	cache := yn(f.CacheWorks)
	if f.CacheRepeatHits > 0 && f.CacheRepeatHits < f.CacheRepeats {
		cache = "partly"
	}
	kv := [][2]string{
		{"streaming", fmt.Sprintf("%s (first byte %s)", yn(f.Streaming), f.TTFB)},
		{"usage reported", yn(f.UsageReported)},
		{"exact cost reported", yn(f.CostReported)},
		{"tool calling", fmt.Sprintf("%s (round trip %s)", yn(f.Tools), yn(f.ToolRoundTrip))},
		{"cached tokens shown", yn(f.CachedTokensReported)},
		{"prefix cache works", fmt.Sprintf("%s (%d of %d repeat requests hit; %.0f%% of their prompt tokens)", cache, f.CacheRepeatHits, f.CacheRepeats, f.CacheHitRatio*100)},
	}
	if f.CacheGranularity > 0 {
		kv = append(kv, [2]string{"cache granularity", fmt.Sprintf("~%d tokens", f.CacheGranularity)})
	}
	if f.MinCachePrefix > 0 {
		kv = append(kv, [2]string{"smallest cached size", fmt.Sprintf("~%d tokens", f.MinCachePrefix)})
	}
	if f.WarmupNeeded != nil {
		kv = append(kv, [2]string{"warm-up before burst", yn(*f.WarmupNeeded)})
	}
	kv = append(kv, [2]string{"reasoning exposed", fmt.Sprintf("%s (structured details %s)", yn(f.ReasoningSeen), yn(f.ReasoningDetails))})
	if f.TokenIDs || f.TokenLogprobs || f.TokenPrefixStable {
		kv = append(kv, [2]string{"token ids", fmt.Sprintf("%s (logprobs %s, prefix-stable for packing %s)", yn(f.TokenIDs), yn(f.TokenLogprobs), yn(f.TokenPrefixStable))})
	}
	if f.RateLimit != "" {
		kv = append(kv, [2]string{"rate limit", f.RateLimit})
	}
	kv = append(kv, [2]string{"bytes per token", fmt.Sprintf("%.2f", f.BytesPerToken)})
	cost := "unavailable"
	switch {
	case rep.CostComplete:
		cost = fmt.Sprintf("$%.6f over %d requests", rep.TotalUSD, len(rep.Steps))
	case f.CostReported:
		cost = fmt.Sprintf("$%.6f reported; total unavailable", rep.TotalUSD)
	}
	kv = append(kv, [2]string{"probe cost", cost})
	for i := range kv {
		kv[i][1] = runner.Clean(kv[i][1])
	}
	notes := append([]string(nil), f.Notes...)
	sort.Strings(notes)
	for i := range notes {
		notes[i] = runner.Clean(notes[i])
	}
	card := &wire.RunCard{Title: "doctor " + runner.Clean(rep.Model), Rows: [][2]string{
		{"streaming", yn(f.Streaming)}, {"tools", yn(f.Tools)},
		{"cache", fmt.Sprintf("%s · %.0f%% of repeated prompts", cache, f.CacheHitRatio*100)},
		{"cost", cost},
	}}
	return &wire.DoctorVerdict{KV: kv, Warn: notes, Card: card}
}

// handleUpdate is GET /api/update: the program's version and the latest release (502 network when GitHub cannot be asked). A build
// from source asks nothing and says how it updates.
func (s *service) handleUpdate(w http.ResponseWriter, r *http.Request) {
	st := wire.UpdateStatus{Current: s.o.Version}
	if update.FromSource(s.o.Version, s.exe()) {
		st.Notes = "This build is from source, not from a release: update it with `go install github.com/anemos-labs/sleipnir/cmd/sleipnir@latest`, or `git pull` and `make build`."
		_ = web.WriteJSON(w, http.StatusOK, st)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	got, err := update.Check(ctx, s.o.Update, s.o.Version, s.o.Commit)
	if err != nil {
		web.Logf(r, "update check: %v", err)
		web.Error(w, http.StatusBadGateway, "network", "could not look for the latest release")
		return
	}
	update.Remember(s.o.Update, got)
	st.Latest = got.Latest
	st.Available = update.Newer(s.o.Version, got.Latest)
	switch {
	case st.Available && got.Behind > 0:
		st.Notes = fmt.Sprintf("%s is %d commits ahead of this build", got.Latest, got.Behind)
	case st.Available:
		st.Notes = got.Latest + " is out"
	default:
		st.Notes = "this is the latest release"
	}
	_ = web.WriteJSON(w, http.StatusOK, st)
}

// exe is the executable an update replaces: the runner's, links resolved.
func (s *service) exe() string {
	if p, err := filepath.EvalSymlinks(s.o.Self); err == nil {
		return p
	}
	return s.o.Self
}

// handleInstall is POST /api/update/install (confirmed for update:<version>, the release it installs): `sleipnir update` in this
// process, its output as run frames. Nothing newer, or a build from source, is 409 none.
func (s *service) handleInstall(w http.ResponseWriter, r *http.Request) {
	exe := s.exe()
	if update.FromSource(s.o.Version, exe) {
		web.Error(w, http.StatusConflict, "none", "this build is from source: update it with go install or git pull")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	got, err := update.Check(ctx, s.o.Update, s.o.Version, s.o.Commit)
	cancel()
	if err != nil {
		web.Logf(r, "update check: %v", err)
		web.Error(w, http.StatusBadGateway, "network", "could not look for the latest release")
		return
	}
	if !update.Newer(s.o.Version, got.Latest) {
		web.Error(w, http.StatusConflict, "none", "sleipnir "+s.o.Version+" is the latest release")
		return
	}
	if !s.srv.RequireConfirm(w, r, "update:"+got.Latest) {
		return
	}
	o, version, commit := s.o.Update, s.o.Version, s.o.Commit
	started, err := s.runs.Start(runner.Spec{
		Path: []string{"update"}, Cmdline: "sleipnir update", Timeout: 10 * time.Minute,
		Func: func(ctx context.Context, stdout, stderr io.Writer) int {
			if err := update.Run(ctx, stdout, o, version, commit, exe, false); err != nil {
				fmt.Fprintln(stderr, "sleipnir:", err)
				return 1
			}
			return 0
		},
	})
	if err != nil {
		web.WriteError(w, err)
		return
	}
	web.Logf(r, "update to %s started as %s", got.Latest, started.ID)
	_ = web.WriteJSON(w, http.StatusAccepted, started)
}
