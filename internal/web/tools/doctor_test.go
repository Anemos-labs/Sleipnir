package tools

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/harden"
	"github.com/anemos-labs/sleipnir/internal/update"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

func TestDoctorEndpointsNameTheKeyVariableNotTheKey(t *testing.T) {
	const name, key = "TOOLSTEST_API_KEY", "sk-test-endpoint-0123456789abcdefgh"
	t.Setenv(name, "")
	harden.Provide(name, key)
	t.Cleanup(func() { harden.Provide(name, "") })
	rg := newRig(t, nil)
	cfg := `{"providers":{"acme":{"base_url":"http://127.0.0.1:9/v1","api_key_env":"` + name + `"}},"models":{"default":"acme/m-1","favorites":["acme/m-2"],"roles":{"scout":"acme/m-3"}}}`
	if err := os.MkdirAll(filepath.Join(rg.home, ".sleipnir"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rg.home, ".sleipnir", "config.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	w := rg.do(req{method: "GET", path: "/api/doctor/endpoints"})
	eps := decode[struct {
		Endpoints []wire.DoctorEndpoint `json:"endpoints"`
	}](t, w).Endpoints
	if len(eps) != 3 || eps[0].Ref != "acme/m-1" || eps[1].Ref != "acme/m-3" || eps[2].Ref != "acme/m-2" || !strings.Contains(eps[0].Where, "$"+name) {
		t.Errorf("endpoints %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), key) {
		t.Error("the key is in the answer")
	}
}

func TestDoctorProbeStreamsStepsAndAVerdict(t *testing.T) {
	const name, key = "TOOLSTEST_API_KEY", "sk-test-doctor-0123456789abcdefghij"
	t.Setenv(name, "")
	harden.Provide(name, key)
	t.Cleanup(func() { harden.Provide(name, "") })
	rg := newRig(t, nil)
	for body, code := range map[string]string{
		`{"baseUrl":"http://127.0.0.1:8089/v1"}`:              "bad_flags",
		`{"model":"--yolo"}`:                                  "bad_flags",
		`{"model":"m","baseUrl":"file:///etc/passwd"}`:        "bad_flags",
		`{"model":"m","baseUrl":"http://u:p@127.0.0.1:1/v1"}`: "bad_flags",
		`{"model":"m","shell":"x"}`:                           "bad_json",
	} {
		if w := rg.do(req{method: "POST", path: "/api/doctor", raw: body}); w.Code != 400 || errCode(w) != code {
			t.Errorf("%s: %d %s", body, w.Code, w.Body.String())
		}
	}
	w := rg.do(req{method: "POST", path: "/api/doctor", body: map[string]any{"model": "m-1", "baseUrl": "http://127.0.0.1:8089/v1", "deep": true}})
	started := decode[wire.RunStarted](t, w)
	if w.Code != http.StatusAccepted || started.Cmdline != "sleipnir doctor --json --model=m-1 --base-url=http://127.0.0.1:8089/v1 --deep" {
		t.Fatalf("doctor: %d %s", w.Code, w.Body.String())
	}
	frames := rg.waitRun(started.ID)
	var steps []wire.DoctorStep
	var end wire.RunFrame
	var text strings.Builder
	for _, f := range frames {
		if f.Step != nil {
			steps = append(steps, *f.Step)
		}
		for _, l := range f.Lines {
			text.WriteString(l.T + "\n")
		}
		if f.Result != nil {
			end = f
		}
	}
	if len(steps) != 3 || steps[0] != (wire.DoctorStep{OK: true, Name: "basic", Ms: 12, Grp: "basic", In: 10, Out: 5}) || steps[2].Cached != 800 || steps[2].Grp != "cache" {
		t.Errorf("steps %+v", steps)
	}
	if end.Verdict == nil || end.Result.Exit != 0 || end.Result.Card == nil {
		t.Fatalf("end %+v", end)
	}
	kv := map[string]string{}
	for _, p := range end.Verdict.KV {
		kv[p[0]] = p[1]
	}
	if kv["streaming"] != "yes (first byte 12ms)" || kv["tool calling"] != "NO (round trip NO)" || !strings.HasPrefix(kv["prefix cache works"], "yes (3 of 3") {
		t.Errorf("verdict %+v", end.Verdict.KV)
	}
	if !strings.Contains(strings.Join(end.Verdict.Warn, "|"), "did not call the offered tool") || !strings.Contains(strings.Join(end.Verdict.Warn, "|"), "basic reply was empty") {
		t.Errorf("warnings %v", end.Verdict.Warn)
	}
	// the probe names its endpoint (--base-url): it runs without the held keys
	if !strings.Contains(text.String(), "probing m-1") || !strings.Contains(text.String(), "key seen: \n") || strings.Contains(text.String(), "\"findings\"") {
		t.Errorf("lines %q", text.String())
	}
	b, _ := json.Marshal(frames)
	if strings.Contains(string(b), key) || strings.Contains(rg.log(), key) {
		t.Error("the key reached the page or the log")
	}
}

func TestUpdateCheckAndInstall(t *testing.T) {
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/releases/latest") {
			_, _ = w.Write([]byte(`{"tag_name":"v9.9.9","html_url":"https://example.invalid/r"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer gh.Close()
	exe := filepath.Join(t.TempDir(), "sleipnir")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	rg := newRig(t, func(o *Options) { o.Update = update.Options{API: gh.URL, HTTP: gh.Client()}; o.Self = exe })
	st := decode[wire.UpdateStatus](t, rg.do(req{method: "GET", path: "/api/update"}))
	if st.Current != "0.1.0" || st.Latest != "v9.9.9" || !st.Available {
		t.Errorf("status %+v", st)
	}
	w := rg.do(req{method: "POST", path: "/api/update/install"})
	if w.Code != http.StatusPreconditionRequired || w.Header().Get("X-Confirm-Scope") != "update:v9.9.9" {
		t.Fatalf("install without a confirmation: %d %q", w.Code, w.Header().Get("X-Confirm-Scope"))
	}
	w = rg.do(req{method: "POST", path: "/api/update/install", header: map[string]string{"X-Confirm": rg.confirm("update:v9.9.9")}})
	started := decode[wire.RunStarted](t, w)
	if w.Code != http.StatusAccepted {
		t.Fatalf("install: %d %s", w.Code, w.Body.String())
	}
	frames := rg.waitRun(started.ID)
	if end := frames[len(frames)-1]; end.Result == nil || end.Result.Exit == 0 {
		t.Errorf("a release without an archive for this machine cannot install: %+v", end)
	}
	if b, _ := os.ReadFile(exe); string(b) != "old" {
		t.Error("a failed install changed the program")
	}

	dev := newRig(t, func(o *Options) { o.Version = "dev"; o.Update = update.Options{API: "http://127.0.0.1:1"} })
	if st := decode[wire.UpdateStatus](t, dev.do(req{method: "GET", path: "/api/update"})); st.Available || !strings.Contains(st.Notes, "from source") {
		t.Errorf("a build from source: %+v", st)
	}
	if w := dev.do(req{method: "POST", path: "/api/update/install"}); w.Code != 409 || errCode(w) != "none" {
		t.Errorf("install from source: %d %s", w.Code, w.Body.String())
	}
	down := newRig(t, func(o *Options) { o.Update = update.Options{API: "http://127.0.0.1:1"}; o.Self = exe })
	if w := down.do(req{method: "GET", path: "/api/update"}); w.Code != http.StatusBadGateway || errCode(w) != "network" {
		t.Errorf("no network: %d %s", w.Code, w.Body.String())
	}
}

// A probe of an endpoint the request names runs without the held keys: a key goes only where the configuration sends it.
func TestDoctorOfACustomEndpointGetsNoKey(t *testing.T) {
	const name, key = "TOOLSTEST_API_KEY", "sk-test-custom-0123456789abcdefghij"
	t.Setenv(name, "")
	harden.Provide(name, key)
	t.Cleanup(func() { harden.Provide(name, "") })
	rg := newRig(t, nil)
	for body, want := range map[string]string{
		`{"model":"m-1","baseUrl":"https://attacker.invalid/v1"}`: "key seen: \n",
		`{"model":"m-1"}`: "key seen: [redacted]\n",
	} {
		w := rg.do(req{method: "POST", path: "/api/doctor", raw: body})
		started := decode[wire.RunStarted](t, w)
		var text strings.Builder
		for _, f := range rg.waitRun(started.ID) {
			for _, l := range f.Lines {
				text.WriteString(l.T + "\n")
			}
		}
		if !strings.Contains(text.String(), want) {
			t.Errorf("%s: %q, want %q", body, text.String(), want)
		}
	}
}
