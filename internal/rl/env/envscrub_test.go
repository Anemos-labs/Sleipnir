package env

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// hostEnv imitates a machine that runs rollouts: full of credentials. The
// values are obviously fake placeholders on purpose.
func hostEnv(path string) []string {
	return []string{
		"PATH=" + path,
		"HOME=/home/operator",
		"OPENAI_API_KEY=placeholder-not-a-secret",
		"ANTHROPIC_API_KEY=placeholder-not-a-secret",
		"AWS_SECRET_ACCESS_KEY=placeholder-not-a-secret",
		"GITHUB_TOKEN=placeholder-not-a-secret",
		"DATABASE_PASSWORD=placeholder-not-a-secret",
		"SSH_AUTH_SOCK=/tmp/agent.1",
		"HTTPS_PROXY=http://127.0.0.1:3128",
		"NO_PROXY=localhost",
		"SSL_CERT_FILE=/etc/ssl/ca.pem",
		"GOROOT=/usr/local/go",
		"GOFLAGS=-mod=mod",
		"GOPROXY=https://proxy.example/",
		"GOCACHE=/home/operator/.cache/go-build",
		"GOPATH=/home/operator/go",
		"RUSTUP_HOME=/opt/rustup",
		"CARGO_HOME=/home/operator/.cargo",
		"LANG=de_DE.UTF-8",
		"TZ=Europe/Berlin",
		"PRIVATE_REGISTRY_TOKEN=placeholder-not-a-secret",
		"ARTIFACTORY_USER=ci",
	}
}

func TestBuildEnvAllowlist(t *testing.T) {
	bin := t.TempDir()
	home := t.TempDir()
	env := BuildEnv(EnvSpec{Home: home, Base: hostEnv(bin), Marker: "run-123"})
	m := EnvMap(env)

	for _, secret := range []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "AWS_SECRET_ACCESS_KEY", "GITHUB_TOKEN",
		"DATABASE_PASSWORD", "SSH_AUTH_SOCK", "PRIVATE_REGISTRY_TOKEN", "CARGO_HOME", "GOCACHE", "GOPATH"} {
		if v, ok := m[secret]; ok {
			t.Errorf("%s=%q leaked into the command environment", secret, v)
		}
	}
	// No network: no proxies, no CA bundle, offline hints.
	for _, k := range []string{"HTTPS_PROXY", "NO_PROXY", "SSL_CERT_FILE"} {
		if _, ok := m[k]; ok {
			t.Errorf("%s passed although the task has no network", k)
		}
	}
	if m["GOPROXY"] != "off" || m["CARGO_NET_OFFLINE"] != "true" {
		t.Errorf("offline hints missing: GOPROXY=%q CARGO_NET_OFFLINE=%q", m["GOPROXY"], m["CARGO_NET_OFFLINE"])
	}
	want := map[string]string{
		"HOME": home, "TMPDIR": home, "TZ": "UTC", "GOROOT": "/usr/local/go", "GOFLAGS": "-mod=mod",
		"RUSTUP_HOME": "/opt/rustup", "GOTOOLCHAIN": "local", "CI": "true", "TERM": "dumb",
		"GIT_TERMINAL_PROMPT": "0", "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": filepath.Join(home, ".gitconfig"),
		"PYTHONDONTWRITEBYTECODE": "1", MarkerEnv: "run-123", "PATH": bin,
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("%s = %q, want %q", k, m[k], v)
		}
	}
	if m["LANG"] == "de_DE.UTF-8" || m["LC_ALL"] == "" {
		t.Errorf("locale not fixed: LANG=%q LC_ALL=%q", m["LANG"], m["LC_ALL"])
	}
	if m["GIT_AUTHOR_NAME"] == "" || strings.Contains(m["GIT_AUTHOR_EMAIL"], "operator") {
		t.Errorf("git identity: %q <%q>", m["GIT_AUTHOR_NAME"], m["GIT_AUTHOR_EMAIL"])
	}
}

func TestBuildEnvNetworkAndPassEnv(t *testing.T) {
	bin := t.TempDir()
	env := BuildEnv(EnvSpec{
		Home: t.TempDir(), Tmp: "/tmp/x", Network: true, Base: hostEnv(bin),
		PassEnv: []string{"PRIVATE_REGISTRY_TOKEN", "ARTIFACTORY_*", "  "},
		Set:     map[string]string{"GOCACHE": "/shared/gocache", "TZ": "Asia/Tokyo"},
	})
	m := EnvMap(env)
	for k, want := range map[string]string{
		"HTTPS_PROXY": "http://127.0.0.1:3128", "NO_PROXY": "localhost", "SSL_CERT_FILE": "/etc/ssl/ca.pem",
		"GOPROXY": "https://proxy.example/", "PRIVATE_REGISTRY_TOKEN": "placeholder-not-a-secret",
		"ARTIFACTORY_USER": "ci", "GOCACHE": "/shared/gocache", "TZ": "Asia/Tokyo", "TMPDIR": "/tmp/x",
	} {
		if m[k] != want {
			t.Errorf("%s = %q, want %q", k, m[k], want)
		}
	}
	if _, ok := m["CARGO_NET_OFFLINE"]; ok {
		t.Error("offline hint set for a task with network")
	}
	// Explicit PassEnv is the only way a credential-shaped name gets through.
	if _, ok := m["OPENAI_API_KEY"]; ok {
		t.Error("unlisted credential leaked")
	}
}

func TestBuildEnvDeterministicAndClean(t *testing.T) {
	bin := t.TempDir()
	spec := EnvSpec{Home: t.TempDir(), Base: hostEnv(bin)}
	a, b := BuildEnv(spec), BuildEnv(spec)
	if !reflect.DeepEqual(a, b) {
		t.Fatal("BuildEnv is not deterministic")
	}
	for i := 1; i < len(a); i++ {
		if a[i-1] >= a[i] {
			t.Fatalf("not sorted: %q before %q", a[i-1], a[i])
		}
	}
	for _, kv := range a {
		if !strings.Contains(kv, "=") || strings.ContainsRune(kv, 0) {
			t.Errorf("malformed entry %q", kv)
		}
	}
	// A NUL in a value cannot smuggle a second variable.
	env := BuildEnv(EnvSpec{Home: t.TempDir(), Base: hostEnv(bin), Set: map[string]string{"X": "a\x00Y=b"}})
	if m := EnvMap(env); m["X"] != "aY=b" || m["Y"] != "" {
		t.Errorf("NUL not stripped: X=%q Y=%q", m["X"], m["Y"])
	}
}

func TestBuildEnvDefaultsToProcessEnvironment(t *testing.T) {
	t.Setenv("GOFLAGS", "-mod=vendor")
	t.Setenv("SOME_API_KEY", "placeholder-not-a-secret")
	m := EnvMap(BuildEnv(EnvSpec{Home: t.TempDir()}))
	if m["GOFLAGS"] != "-mod=vendor" {
		t.Errorf("GOFLAGS = %q", m["GOFLAGS"])
	}
	if _, ok := m["SOME_API_KEY"]; ok {
		t.Error("credential leaked from the process environment")
	}
	if m["PATH"] == "" {
		t.Error("PATH is empty")
	}
}

func TestSanitizePath(t *testing.T) {
	good := t.TempDir()
	good2 := t.TempDir()
	workspace := t.TempDir()
	inWorkspace := filepath.Join(workspace, "node_modules", ".bin")
	if err := os.MkdirAll(inWorkspace, 0o755); err != nil {
		t.Fatal(err)
	}
	worldWritable := t.TempDir()
	if err := os.Chmod(worldWritable, 0o777); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(good, "afile")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	p := strings.Join([]string{
		"", ".", "relative/bin", "../bin", good, good + "/", good2, file,
		filepath.Join(good, "does-not-exist"), worldWritable, inWorkspace, workspace, good,
	}, string(os.PathListSeparator))
	got := sanitizePath(p, []string{workspace}, runtime.GOOS, os.Getenv("SYSTEMROOT"))
	want := []string{good, good2}
	if runtime.GOOS == "windows" {
		// The mode bits are meaningless there (every directory reads as 0777), so they decide nothing.
		want = append(want, worldWritable)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sanitizePath = %q, want %q", got, want)
	}
	// Nothing usable: fall back to system directories, never to an empty PATH.
	got = sanitizePath("."+string(os.PathListSeparator)+"relative", nil, runtime.GOOS, os.Getenv("SYSTEMROOT"))
	if len(got) == 0 {
		t.Fatal("empty PATH")
	}
	for _, d := range got {
		if !filepath.IsAbs(d) {
			t.Errorf("relative entry %q", d)
		}
	}
}

func TestUnderAny(t *testing.T) {
	n := filepath.FromSlash
	if !underAny(n("/a/b/c"), []string{n("/x"), n("/a/b")}) || !underAny(n("/a/b"), []string{n("/a/b/")}) {
		t.Error("expected match")
	}
	if underAny(n("/a/bc"), []string{n("/a/b")}) || underAny(n("/a"), []string{n("/a/b"), ""}) {
		t.Error("prefix must respect path boundaries")
	}
	if runtime.GOOS == "windows" && !underAny(`c:\work\ws\bin`, []string{`C:\Work`}) {
		t.Error("Windows paths differ in case only and name the same directory")
	}
}

// TestBuildEnvWindows: a Windows host spells PATH "Path" and needs its system variables to start anything; the
// command still sees no credential, and its temporary and profile directories are the private ones.
func TestBuildEnvWindows(t *testing.T) {
	bin, home, tmp := t.TempDir(), t.TempDir(), t.TempDir()
	base := []string{
		"Path=" + bin, "SystemRoot=C:\\Windows", "windir=C:\\Windows", "ComSpec=C:\\Windows\\system32\\cmd.exe",
		"PATHEXT=.COM;.EXE;.BAT;.CMD", "ProgramFiles=C:\\Program Files", "NUMBER_OF_PROCESSORS=8",
		"TEMP=C:\\Users\\operator\\AppData\\Local\\Temp", "USERPROFILE=C:\\Users\\operator",
		"LOCALAPPDATA=C:\\Users\\operator\\AppData\\Local", "OPENAI_API_KEY=placeholder-not-a-secret",
		"Private_Registry_Token=placeholder-not-a-secret",
	}
	m := EnvMap(buildEnv(EnvSpec{Home: home, Tmp: tmp, Base: base, PassEnv: []string{"private_registry_*"}}, "windows"))
	want := map[string]string{
		"PATH": bin, "SYSTEMROOT": `C:\Windows`, "WINDIR": `C:\Windows`, "COMSPEC": `C:\Windows\system32\cmd.exe`,
		"PATHEXT": ".COM;.EXE;.BAT;.CMD", "PROGRAMFILES": `C:\Program Files`, "NUMBER_OF_PROCESSORS": "8",
		"TEMP": tmp, "TMP": tmp, "TMPDIR": tmp, "HOME": home, "USERPROFILE": home,
		"LOCALAPPDATA": filepath.Join(home, "AppData", "Local"), "APPDATA": filepath.Join(home, "AppData", "Roaming"),
		"PYTHONUTF8": "1", "PRIVATE_REGISTRY_TOKEN": "placeholder-not-a-secret",
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("%s = %q, want %q", k, m[k], v)
		}
	}
	for _, k := range []string{"OPENAI_API_KEY", "Path"} {
		if v, ok := m[k]; ok {
			t.Errorf("%s=%q should not be in the command environment", k, v)
		}
	}
	// A forced value replaces the variable whatever its case; two spellings of one name never both reach the child.
	forced := buildEnv(EnvSpec{Home: home, Tmp: tmp, Base: base, Set: map[string]string{"Path": `D:\tools`, "Comspec": `D:\cmd.exe`}}, "windows")
	seen := map[string]int{}
	for _, kv := range forced {
		k, _, _ := strings.Cut(kv, "=")
		seen[strings.ToUpper(k)]++
	}
	for k, n := range seen {
		if n != 1 {
			t.Errorf("%s appears %d times in %v", k, n, forced)
		}
	}
	if f := EnvMap(forced); f["PATH"] != `D:\tools` || f["COMSPEC"] != `D:\cmd.exe` || f["Path"] != "" || f["Comspec"] != "" {
		t.Errorf("a mixed-case Set did not replace the variable: %v", f)
	}
	// The Unix build leaves Windows variables alone, and its names keep their case.
	if u := EnvMap(buildEnv(EnvSpec{Home: home, Base: base, Set: map[string]string{"Path": "x"}}, "linux")); u["Path"] != "x" {
		t.Errorf("linux folded a forced name: %v", u)
	}
	if u := EnvMap(buildEnv(EnvSpec{Home: home, Base: base}, "linux")); u["SYSTEMROOT"] != "" || u["TEMP"] != "" || u["PATH"] == bin {
		t.Errorf("linux environment took Windows variables: %v", u)
	}
}

// TestBuildEnvKeepsTheHostsToolsOnWindows runs against the real host: the scrubbed PATH must still reach the system
// directory, or every verifier exits 127.
func TestBuildEnvKeepsTheHostsToolsOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows only")
	}
	cmd, err := exec.LookPath("cmd")
	if err != nil {
		t.Skip("no cmd.exe on PATH")
	}
	m := EnvMap(BuildEnv(EnvSpec{Home: t.TempDir()}))
	found := false
	for _, d := range filepath.SplitList(m["PATH"]) {
		if strings.EqualFold(d, filepath.Dir(cmd)) {
			found = true
		}
	}
	if !found {
		t.Errorf("PATH %q lost %s", m["PATH"], filepath.Dir(cmd))
	}
	if m["SYSTEMROOT"] == "" || m["PATHEXT"] == "" || m["COMSPEC"] == "" {
		t.Errorf("system variables missing: SYSTEMROOT=%q PATHEXT=%q COMSPEC=%q", m["SYSTEMROOT"], m["PATHEXT"], m["COMSPEC"])
	}
}
