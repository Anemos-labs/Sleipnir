package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/mcp/mcptest"
	"github.com/reee344/sleipnir/internal/perm"
	"github.com/reee344/sleipnir/internal/tools"
)

// adapterFixture is a manager over one reference server ("srv") behind HTTP.
type adapterFixture struct {
	m   *Manager
	s   *mcptest.Server
	tls []tools.Tool
}

func newAdapterFixture(t *testing.T, mod func(*ServerConfig), mopt func(*Options)) *adapterFixture {
	t.Helper()
	s := mcptest.New()
	ts, _ := httpServer(t, s, mcptest.HTTPOptions{JSONOnly: true})
	cfg := httpCfg(ts.URL)
	if mod != nil {
		mod(&cfg)
	}
	o := quickOpts(map[string]ServerConfig{"srv": cfg})
	if mopt != nil {
		mopt(&o)
	}
	m := startManager(t, o)
	return &adapterFixture{m: m, s: s, tls: m.Tools()}
}

func (f *adapterFixture) tool(t *testing.T, suffix string) tools.Tool {
	return findTool(t, f.tls, suffix)
}

func TestAdapterRunsATool(t *testing.T) {
	f := newAdapterFixture(t, nil, nil)
	rec := newRecorder(true)
	env, _ := testEnv(rec)
	res := runTool(t, f.tool(t, "__echo"), env, `{"message":"hello from the model"}`)
	if res.IsError || res.Text != "hello from the model" {
		t.Fatalf("%+v", res)
	}
	if res.Meta["mcp_server"] != "srv" || res.Meta["mcp_tool"] != "echo" {
		t.Errorf("meta = %v", res.Meta)
	}
	if f.s.Calls("echo") != 1 {
		t.Errorf("server saw %d calls", f.s.Calls("echo"))
	}
}

func TestAdapterAsksPermissionWithADescriptiveRequest(t *testing.T) {
	skipNotUnix(t)
	mk := func(t *testing.T) (*Manager, *mcptest.Server) {
		s := mcptest.New()
		ts, _ := httpServer(t, s, mcptest.HTTPOptions{JSONOnly: true})
		return startManager(t, quickOpts(map[string]ServerConfig{"srv": httpCfg(ts.URL)})), s
	}
	t.Run("remote", func(t *testing.T) {
		m, _ := mk(t)
		rec := newRecorder(true)
		env, _ := testEnv(rec)
		tls := m.Tools()
		runTool(t, findTool(t, tls, "__echo"), env, `{"message":"a  \n  b"}`)
		runTool(t, findTool(t, tls, "__big"), env, `{"bytes":10}`)
		reqs := rec.requests()
		if len(reqs) != 2 {
			t.Fatalf("%d permission requests", len(reqs))
		}
		echo, big := reqs[0], reqs[1]
		if echo.Tool != "mcp__srv__echo" || big.Tool != "mcp__srv__big" {
			t.Errorf("tools = %s %s", echo.Tool, big.Tool)
		}
		if echo.Agent != "a1" || echo.Role != "worker" {
			t.Errorf("agent/role = %q %q", echo.Agent, echo.Role)
		}
		if string(echo.Input) != `{"message":"a  \n  b"}` {
			t.Errorf("input = %s", echo.Input)
		}
		if !echo.Network || echo.Risk != perm.RiskMedium {
			t.Errorf("a remote MCP call is a network request of medium risk: %+v", echo)
		}
		if echo.Writes {
			t.Error("echo carries readOnlyHint: true, so it does not write")
		}
		if !big.Writes {
			t.Error("without a readOnlyHint the effects are unknown, so the request must say it writes")
		}
		for _, r := range reqs {
			if strings.ContainsAny(r.Summary, "\n\r\t") || !strings.HasPrefix(r.Summary, "MCP srv/") || len(r.Summary) > 260 {
				t.Errorf("summary = %q", r.Summary)
			}
		}
		if !strings.Contains(echo.Summary, "message") {
			t.Errorf("the summary should show what is being asked: %q", echo.Summary)
		}
	})
	t.Run("local process is not a network request", func(t *testing.T) {
		m := startManager(t, quickOpts(map[string]ServerConfig{"srv": helperCfg("", nil)}))
		rec := newRecorder(true)
		env, _ := testEnv(rec)
		runTool(t, findTool(t, m.Tools(), "__echo"), env, `{"message":"x"}`)
		if r := rec.requests()[0]; r.Network {
			t.Errorf("stdio server flagged as network: %+v", r)
		}
	})
}

func TestAdapterDeniedCallNeverReachesTheServer(t *testing.T) {
	f := newAdapterFixture(t, nil, nil)
	rec := newRecorder(false)
	rec.why = "plan mode: MCP tools may change state\nrun in default mode"
	env, _ := testEnv(rec)
	res := runTool(t, f.tool(t, "__echo"), env, `{"message":"x"}`)
	if !res.IsError || !strings.Contains(res.Text, "permission denied") || !strings.Contains(res.Text, "plan mode") {
		t.Errorf("%+v", res)
	}
	if strings.Contains(res.Text, "\n") {
		t.Errorf("the reason should be one line: %q", res.Text)
	}
	if f.s.Calls("echo") != 0 {
		t.Error("a denied call reached the server")
	}
	// An empty reason still yields an actionable message.
	rec2 := &recorder{allow: false}
	env2, _ := testEnv(rec2)
	if res := runTool(t, f.tool(t, "__echo"), env2, `{}`); !strings.Contains(res.Text, "not allowed") {
		t.Errorf("%+v", res)
	}
}

func TestAdapterFailsClosedWithoutAPermissionChecker(t *testing.T) {
	f := newAdapterFixture(t, nil, nil)
	tl := f.tool(t, "__echo")
	for name, env := range map[string]*tools.Env{"nil env": nil, "nil checker": {Agent: "a"}} {
		res, err := tl.Run(context.Background(), &tools.Call{Name: "mcp__srv__echo", Input: json.RawMessage(`{"message":"x"}`), Env: env})
		if err != nil || res == nil || !res.IsError || !strings.Contains(res.Text, "no permission checker") {
			t.Errorf("%s: %+v %v", name, res, err)
		}
	}
	if f.s.Calls("echo") != 0 {
		t.Error("an unchecked call reached the server")
	}
}

func TestAdapterPermissionCheckerPanicIsContained(t *testing.T) {
	f := newAdapterFixture(t, nil, nil)
	env, _ := testEnv(permFunc(func(perm.Request) perm.Decision { panic("engine bug") }))
	res := runTool(t, f.tool(t, "__echo"), env, `{"message":"x"}`)
	if !res.IsError || !strings.Contains(res.Text, "failed unexpectedly") {
		t.Errorf("%+v", res)
	}
}

func TestAdapterBadArguments(t *testing.T) {
	f := newAdapterFixture(t, nil, nil)
	env, _ := testEnv(newRecorder(true))
	tl := f.tool(t, "__echo")
	tests := []struct {
		name, in, want string
	}{
		{"array", `[1]`, "must be a JSON object"},
		{"string", `"x"`, "must be a JSON object"},
		{"number", `5`, "must be a JSON object"},
		{"truncated json", `{"message":`, "must be a JSON object"},
		{"too large", `{"message":"` + strings.Repeat("x", maxInputBytes) + `"}`, "too large"},
	}
	for _, tt := range tests {
		res := runTool(t, tl, env, tt.in)
		if !res.IsError || !strings.Contains(res.Text, tt.want) {
			t.Errorf("%s: %+v", tt.name, res)
		}
	}
	if f.s.Calls("echo") != 0 {
		t.Error("bad arguments reached the server")
	}
	// Empty and null arguments mean "no arguments".
	for _, in := range []string{``, `null`, `  {}  `} {
		if res := runTool(t, tl, env, in); res.IsError {
			t.Errorf("%q: %+v", in, res)
		}
	}
}

func TestAdapterServerErrorsAreModelVisibleResults(t *testing.T) {
	f := newAdapterFixture(t, nil, nil)
	env, _ := testEnv(newRecorder(true))
	f.s.AddTool(mcptest.Tool{Name: "slowpoke", Description: "s", Handler: func(ctx context.Context, c *mcptest.Call) *mcptest.Result {
		<-ctx.Done()
		return &mcptest.Result{Drop: true}
	}})
	f.s.NotifyToolsChanged()
	m2 := f.m
	waitFor(t, "slowpoke", func() bool { return containsAll(m2.Preview().Names(), "mcp__srv__slowpoke") })
	pv := m2.Preview()

	t.Run("isError result", func(t *testing.T) {
		res := runTool(t, f.tool(t, "__error"), env, `{}`)
		if !res.IsError || res.Text != "boom" {
			t.Errorf("%+v", res)
		}
	})
	t.Run("unknown tool", func(t *testing.T) {
		f.s.RemoveTool("pid")
		res := runTool(t, f.tool(t, "__pid"), env, `{}`)
		if !res.IsError || !strings.Contains(res.Text, "rejected the call") || !strings.Contains(res.Text, "Unknown tool") {
			t.Errorf("%+v", res)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		cfg := f.m.serverByName("srv")
		cfg.mu.Lock()
		cfg.cfg.Timeout = 200 * time.Millisecond
		cfg.mu.Unlock()
		defer func() { cfg.mu.Lock(); cfg.cfg.Timeout = 0; cfg.mu.Unlock() }()
		start := time.Now()
		res := runTool(t, findTool(t, pv.Tools(), "__slowpoke"), env, `{}`)
		if !res.IsError || !strings.Contains(res.Text, "did not respond in time") {
			t.Errorf("%+v", res)
		}
		if time.Since(start) > 5*time.Second {
			t.Error("timeout ignored")
		}
	})
	t.Run("context cancelled by the agent", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		defer cancel()
		res, err := findTool(t, pv.Tools(), "__slowpoke").Run(ctx, &tools.Call{Input: json.RawMessage(`{}`), Env: env})
		if err != nil || !res.IsError {
			t.Fatalf("%+v %v", res, err)
		}
	})
	t.Run("oversized result", func(t *testing.T) {
		f2 := newAdapterFixture(t, nil, func(o *Options) { o.MaxMessageBytes = 64 << 10 })
		res := runTool(t, f2.tool(t, "__big"), env, `{"bytes":500000}`)
		if !res.IsError || !strings.Contains(res.Text, "too large") {
			t.Errorf("%+v", res)
		}
	})
}

func TestAdapterStdioCrashIsAModelVisibleResult(t *testing.T) {
	skipNotUnix(t)
	m := startManager(t, quickOpts(map[string]ServerConfig{"srv": helperCfg("", nil)}))
	env, _ := testEnv(newRecorder(true))
	res := runTool(t, findTool(t, m.Tools(), "__crash"), env, `{}`)
	if !res.IsError || !strings.Contains(res.Text, "connection was lost") || !strings.Contains(res.Text, "exit status 3") {
		t.Errorf("%+v", res)
	}
}

func TestAdapterTruncatesWithARecallHandle(t *testing.T) {
	f := newAdapterFixture(t, nil, nil)
	rec := newRecorder(true)
	env, blobs := testEnv(rec)
	env.Limits.MaxOutputChars = 1000
	res := runTool(t, f.tool(t, "__big"), env, `{"bytes":100000}`)
	if res.IsError || !res.Truncated || res.Handle == "" || res.FullRef == "" {
		t.Fatalf("truncated=%v handle=%q ref=%q", res.Truncated, res.Handle, res.FullRef)
	}
	if len(res.Text) > 1400 || !strings.Contains(res.Text, "chars elided") || !strings.Contains(res.Text, res.Handle) {
		t.Errorf("text (%d bytes) = %.200q", len(res.Text), res.Text)
	}
	ref, n, ok := env.Handles.Resolve(res.Handle)
	if !ok || n != 100000 {
		t.Fatalf("handle resolves to %v %d", ok, n)
	}
	full, err := blobs.Get(ref)
	if err != nil || len(full) != 100000 || string(full) != strings.Repeat("x", 100000) {
		t.Errorf("the full output must be recoverable: %d bytes, %v", len(full), err)
	}
}

func TestAdapterServerCanLowerButNeverRaiseTheOutputLimit(t *testing.T) {
	for _, tt := range []struct {
		name      string
		cfgLimit  int
		envLimit  int
		wantLimit int
	}{
		{"server lowers", 300, 1000, 300},
		{"server cannot raise", 50000, 1000, 1000},
		{"unset follows the harness", 0, 1000, 1000},
		{"harness unlimited, server limits", 400, -1, 400},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newAdapterFixture(t, func(c *ServerConfig) { c.MaxOutputChars = tt.cfgLimit }, nil)
			env, _ := testEnv(newRecorder(true))
			env.Limits.MaxOutputChars = tt.envLimit
			res := runTool(t, f.tool(t, "__big"), env, `{"bytes":100000}`)
			if !res.Truncated {
				t.Fatal("not truncated")
			}
			body := res.Text
			if i := strings.Index(body, "\n[full output"); i >= 0 {
				body = body[:i]
			}
			if n := strings.Count(body, "x"); n > tt.wantLimit || n < tt.wantLimit-20 {
				t.Errorf("%d visible characters, want about %d", n, tt.wantLimit)
			}
		})
	}
}

func TestAdapterImagesAreStoredNotShown(t *testing.T) {
	png, _ := decodeBase64("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR4nGNgYGD4DwABBAEAwS2OUAAAAABJRU5ErkJggg==")
	t.Run("placeholder only by default", func(t *testing.T) {
		f := newAdapterFixture(t, nil, nil)
		env, blobs := testEnv(newRecorder(true))
		res := runTool(t, f.tool(t, "__image"), env, `{}`)
		if res.IsError || !strings.Contains(res.Text, "[image: image/png,") || !strings.Contains(res.Text, "bytes") || !strings.Contains(res.Text, "stored as blob") || !strings.Contains(res.Text, "not shown") {
			t.Fatalf("text = %q", res.Text)
		}
		if len(res.Blocks) != 0 {
			t.Errorf("media must not reach the model unless the harness opts in: %+v", res.Blocks)
		}
		media, _ := res.Meta["media"].([]mediaRef)
		if len(media) != 1 || media[0].Kind != "image" || media[0].MediaType != "image/png" || media[0].Bytes != len(png) || media[0].Ref == "" {
			t.Fatalf("meta = %+v", res.Meta)
		}
		got, err := blobs.Get(core.Hash(media[0].Ref))
		if err != nil || string(got) != string(png) {
			t.Errorf("stored bytes differ: %v", err)
		}
		if strings.Contains(res.Text, "iVBOR") {
			t.Error("base64 payload leaked into the model-visible text")
		}
	})
	t.Run("opt-in attaches a block", func(t *testing.T) {
		f := newAdapterFixture(t, nil, func(o *Options) { o.AttachMedia = true })
		env, _ := testEnv(newRecorder(true))
		res := runTool(t, f.tool(t, "__image"), env, `{}`)
		if len(res.Blocks) != 1 || res.Blocks[0].Kind != core.BlockImage || res.Blocks[0].MediaType != "image/png" || res.Blocks[0].MediaRef == "" {
			t.Errorf("%+v", res.Blocks)
		}
	})
	t.Run("no blob store", func(t *testing.T) {
		f := newAdapterFixture(t, nil, nil)
		env, _ := testEnv(newRecorder(true))
		env.Blobs = nil
		res := runTool(t, f.tool(t, "__image"), env, `{}`)
		if res.IsError || !strings.Contains(res.Text, "not stored") {
			t.Errorf("%+v", res)
		}
	})
}

func TestAdapterRendersEveryContentType(t *testing.T) {
	f := newAdapterFixture(t, nil, nil)
	env, blobs := testEnv(newRecorder(true))

	t.Run("links, embedded resources, audio", func(t *testing.T) {
		res := runTool(t, f.tool(t, "__links"), env, `{}`)
		for _, want := range []string{
			"[resource link: readme file:///readme.md (text/markdown): The readme]",
			"[resource file:///note.txt (text/plain)]", "embedded note",
			"[resource mem://blob (application/octet-stream)]", "[blob: application/octet-stream, 3 bytes, stored as blob",
			"[audio: audio/wav, 12 bytes, stored as blob",
		} {
			if !strings.Contains(res.Text, want) {
				t.Errorf("text lacks %q:\n%s", want, res.Text)
			}
		}
		if blobs.Len() < 2 {
			t.Errorf("%d blobs stored", blobs.Len())
		}
	})
	t.Run("structured content is not printed twice", func(t *testing.T) {
		res := runTool(t, f.tool(t, "__structured"), env, `{}`)
		if res.Text != `{"answer":42}` || res.Meta["structured"] != true {
			t.Errorf("%+v", res)
		}
	})
	t.Run("structured content without a text twin is rendered", func(t *testing.T) {
		f.s.AddTool(mcptest.Tool{Name: "only_structured", Handler: func(context.Context, *mcptest.Call) *mcptest.Result {
			return &mcptest.Result{Structured: map[string]any{"b": 2, "a": 1}}
		}})
		f.s.NotifyToolsChanged()
		waitFor(t, "tool", func() bool { return containsAll(f.m.Preview().Names(), "mcp__srv__only_structured") })
		res := runTool(t, findTool(t, f.m.Preview().Tools(), "__only_structured"), env, `{}`)
		if res.Text != `structuredContent: {"a":1,"b":2}` {
			t.Errorf("%q", res.Text)
		}
	})
	t.Run("unknown content types are named and skipped", func(t *testing.T) {
		f.s.AddTool(mcptest.Tool{Name: "future", Handler: func(context.Context, *mcptest.Call) *mcptest.Result {
			return &mcptest.Result{Content: []map[string]any{{"type": "hologram", "x": 1}, mcptest.Text("but also text")}}
		}})
		f.s.NotifyToolsChanged()
		waitFor(t, "tool", func() bool { return containsAll(f.m.Preview().Names(), "mcp__srv__future") })
		res := runTool(t, findTool(t, f.m.Preview().Tools(), "__future"), env, `{}`)
		if !strings.Contains(res.Text, `[content of type "hologram" omitted`) || !strings.Contains(res.Text, "but also text") {
			t.Errorf("%q", res.Text)
		}
	})
	t.Run("an empty result is not an error", func(t *testing.T) {
		f.s.AddTool(mcptest.Tool{Name: "nothing", Handler: func(context.Context, *mcptest.Call) *mcptest.Result { return &mcptest.Result{} }})
		f.s.NotifyToolsChanged()
		waitFor(t, "tool", func() bool { return containsAll(f.m.Preview().Names(), "mcp__srv__nothing") })
		res := runTool(t, findTool(t, f.m.Preview().Tools(), "__nothing"), env, `{}`)
		if res.IsError || res.Text != "" {
			t.Errorf("%+v", res)
		}
	})
}

func TestAdapterSanitisesResults(t *testing.T) {
	f := newAdapterFixture(t, nil, nil)
	env, _ := testEnv(newRecorder(true))
	res := runTool(t, f.tool(t, "__unicode"), env, `{}`)
	if res.Text != "plain red  zerowidth flipped  end" {
		t.Errorf("text = %q", res.Text)
	}
	for _, r := range res.Text {
		if invisible(r) {
			t.Errorf("invisible %U in result", r)
		}
	}
}

func TestAdapterRedactsCredentialsEchoedByAServer(t *testing.T) {
	skipNotUnix(t)
	cfg := helperCfg("", map[string]string{"SERVICE_TOKEN": placeholder + "-echoed"})
	m := startManager(t, quickOpts(map[string]ServerConfig{"srv": cfg}))
	env, _ := testEnv(newRecorder(true))
	res := runTool(t, findTool(t, m.Tools(), "__env"), env, `{}`)
	if strings.Contains(res.Text, placeholder) {
		t.Errorf("the server echoed its own credential and it reached the model:\n%s", res.Text)
	}
	if !strings.Contains(res.Text, "SERVICE_TOKEN=***") {
		t.Errorf("redaction should mask the value, not drop the line:\n%s", res.Text)
	}
}

func TestAdapterProgressGoesToTheUI(t *testing.T) {
	f := newAdapterFixture(t, nil, nil)
	f.s.AddTool(mcptest.Tool{Name: "steps", Handler: func(_ context.Context, c *mcptest.Call) *mcptest.Result {
		return mcptest.TextResult("no progress over json")
	}})
	// Progress needs a streaming transport: use an SSE-mode server for this one.
	s := mcptest.New()
	ts, _ := httpServer(t, s, mcptest.HTTPOptions{})
	m := startManager(t, quickOpts(map[string]ServerConfig{"srv": httpCfg(ts.URL)}))
	env, _ := testEnv(newRecorder(true))
	var mu sync.Mutex
	var lines []string
	env.Out = func(stream, text string) {
		mu.Lock()
		lines = append(lines, stream+": "+text)
		mu.Unlock()
	}
	res := runTool(t, findTool(t, m.Tools(), "__progress"), env, `{"steps":3}`)
	if res.IsError || res.Text != "progressed" {
		t.Fatal(res)
	}
	waitFor(t, "a progress line", func() bool { mu.Lock(); defer mu.Unlock(); return len(lines) > 0 })
	mu.Lock()
	defer mu.Unlock()
	if !strings.HasPrefix(lines[0], "info: mcp__srv__progress: progress 1/3 step 1") {
		t.Errorf("lines = %v", lines)
	}
	if len(lines) > 3 {
		t.Errorf("%d progress lines", len(lines))
	}
}

func TestAdapterSpecAndAnnotations(t *testing.T) {
	f := newAdapterFixture(t, nil, nil)
	echo := f.tool(t, "__echo")
	sp := echo.Spec()
	if sp.Name != "mcp__srv__echo" || !sp.ReadOnly || sp.Description != "Echo the message back." {
		t.Errorf("%+v", sp)
	}
	if !strings.Contains(string(sp.InputSchema), `"message"`) {
		t.Errorf("schema = %s", sp.InputSchema)
	}
	if f.tool(t, "__big").Spec().ReadOnly {
		t.Error("a tool without a readOnlyHint is not read-only")
	}
	// Spec returns copies.
	sp.InputSchema[0] = 'X'
	sp.Description = "changed"
	if again := echo.Spec(); again.InputSchema[0] != '{' || again.Description != "Echo the message back." {
		t.Error("Spec exposes internal state")
	}
}

func TestAdapterDisabledToolFailsEvenWithAStaleAdapter(t *testing.T) {
	f := newAdapterFixture(t, nil, nil)
	env, _ := testEnv(newRecorder(true))
	srv := f.m.serverByName("srv")
	srv.mu.Lock()
	srv.cfg.DenyTools = []string{"echo"} // configuration changed after the snapshot
	srv.mu.Unlock()
	res := runTool(t, f.tool(t, "__echo"), env, `{"message":"x"}`)
	if !res.IsError || !strings.Contains(res.Text, "disabled by the MCP server configuration") {
		t.Errorf("%+v", res)
	}
	if f.s.Calls("echo") != 0 {
		t.Error("a denied tool reached the server")
	}
}

func TestAdapterAfterManagerClose(t *testing.T) {
	f := newAdapterFixture(t, nil, nil)
	f.m.Close()
	env, _ := testEnv(newRecorder(true))
	res := runTool(t, f.tool(t, "__echo"), env, `{"message":"x"}`)
	if !res.IsError || !strings.Contains(res.Text, "not connected") {
		t.Errorf("%+v", res)
	}
}

func TestAdapterConcurrentRunsAreRaceFree(t *testing.T) {
	f := newAdapterFixture(t, nil, nil)
	env, _ := testEnv(newRecorder(true))
	tl := f.tool(t, "__echo")
	var wg sync.WaitGroup
	var bad atomic.Int32
	for g := 0; g < 20; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 10; i++ {
				msg := fmt.Sprintf("g%d-i%d", g, i)
				b, _ := json.Marshal(map[string]string{"message": msg})
				res, err := tl.Run(context.Background(), &tools.Call{Input: b, Env: env})
				if err != nil || res.IsError || res.Text != msg {
					bad.Add(1)
				}
			}
		}(g)
	}
	wg.Wait()
	if bad.Load() != 0 {
		t.Errorf("%d bad results", bad.Load())
	}
}

func TestAdapterNeverReturnsAGoError(t *testing.T) {
	f := newAdapterFixture(t, nil, nil)
	tl := f.tool(t, "__echo")
	rec := newRecorder(true)
	env, _ := testEnv(rec)
	inputs := []string{``, `null`, `{}`, `[`, `{"message":1}`, strings.Repeat("{", 100)}
	for _, in := range inputs {
		res, err := tl.Run(context.Background(), &tools.Call{Input: json.RawMessage(in), Env: env})
		if err != nil || res == nil {
			t.Errorf("input %.20q: res=%v err=%v", in, res, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := tl.Run(ctx, &tools.Call{Input: json.RawMessage(`{}`), Env: env})
	if err != nil || res == nil || !res.IsError {
		t.Errorf("cancelled: %v %v", res, err)
	}
}
