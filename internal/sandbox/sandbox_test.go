package sandbox

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestMain lets the test binary be the exec helper, as the sleipnir binary is for a session.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == ExecCommand {
		os.Exit(ExecMain(os.Args[2:]))
	}
	os.Exit(m.Run())
}

func TestParse(t *testing.T) {
	for _, c := range []struct {
		in   string
		want Spec
		bad  bool
	}{
		{"", Spec{}, false},
		{"none", Spec{}, false},
		{"docker", Spec{Engine: "docker"}, false},
		{"docker=node:22-bookworm", Spec{Engine: "docker", Image: "node:22-bookworm"}, false},
		{"docker=ghcr.io/acme/dev@sha256:abc", Spec{Engine: "docker", Image: "ghcr.io/acme/dev@sha256:abc"}, false},
		{"podman", Spec{}, true},
		{"docker=--privileged", Spec{}, true},
		{"docker=img;rm -rf /", Spec{}, true},
	} {
		got, err := Parse(c.in)
		if (err != nil) != c.bad {
			t.Fatalf("Parse(%q) error = %v, want error %v", c.in, err, c.bad)
		}
		if !c.bad && got != c.want {
			t.Fatalf("Parse(%q) = %+v, want %+v", c.in, got, c.want)
		}
	}
	if s := (Spec{Engine: "docker"}).String(); s != "docker" {
		t.Fatalf("String of the chosen image = %q", s)
	}
	if s := (Spec{Engine: "docker", Image: DefaultImage}).String(); s != "docker="+DefaultImage {
		t.Fatalf("String of a named default image = %q: it would read back as the image chosen for the project", s)
	}
	if s := (Spec{Engine: "docker", Image: "alpine:3"}).String(); s != "docker=alpine:3" {
		t.Fatalf("String = %q", s)
	}
}

// The container is confined: no network unless asked, no capabilities, no privilege escalation, limits, the host user, and the
// image's entrypoint replaced.
func TestRunArgsConfine(t *testing.T) {
	a := RunArgs("n", "img", false, []string{"/p"}, "/p", DefaultLimits, 1000, 1000, 42)
	line := strings.Join(a, " ")
	for _, want := range []string{"--network none", "--cap-drop ALL", "--security-opt no-new-privileges", "--pids-limit 2048",
		"--memory 8g", "--user 1000:1000", "--volume /p:/p", "--workdir /p", "--init", "--rm", "--entrypoint sleep img infinity", Label + ".owner=42"} {
		if !strings.Contains(line, want) {
			t.Fatalf("run args lack %q: %s", want, line)
		}
	}
	if slices.Contains(a, "--privileged") {
		t.Fatal("privileged container")
	}
	if a := RunArgs("n", "img", true, nil, "", DefaultLimits, 1, 1, 1); !strings.Contains(strings.Join(a, " "), "--network bridge") {
		t.Fatal("network asked for and not given")
	}
}

// Only the allowlisted variables cross into the container: a key or a host PATH does not.
func TestExecArgsPassOnlyFormatting(t *testing.T) {
	a := ExecArgs("box", "/w", "id1", []string{"TERM=xterm", "PATH=/host/bin", "OPENAI_API_KEY=sk-x", "HOME=/home/me", "NO_COLOR=1"}, []string{"bash", "-c", "true"})
	line := strings.Join(a, " ")
	if !strings.Contains(line, "--env TERM=xterm") || !strings.Contains(line, "--env NO_COLOR=1") {
		t.Fatalf("formatting variables missing: %s", line)
	}
	for _, leak := range []string{"PATH=", "OPENAI_API_KEY", "HOME="} {
		if strings.Contains(line, leak) {
			t.Fatalf("%s crossed into the container: %s", leak, line)
		}
	}
	if !slices.Equal(a[len(a)-3:], []string{"bash", "-c", "true"}) {
		t.Fatalf("program not last: %v", a)
	}
}

// startBox starts a real sandbox over dir, or skips when no container engine answers.
func startBox(t *testing.T, dir string, network bool, image ...string) *Box {
	t.Helper()
	if testing.Short() {
		t.Skip("starts a container")
	}
	if runtime.GOOS == "windows" {
		t.Skip("a sandbox needs a Unix host: it mounts the project at the same path inside")
	}
	ctx := context.Background()
	if err := Available(ctx); err != nil {
		t.Skip("no container engine: ", err)
	}
	img := os.Getenv("SLEIPNIR_TEST_SANDBOX_IMAGE")
	if img == "" {
		img = "alpine:3.20"
	}
	if len(image) > 0 {
		img = image[0] // "" is the image chosen for the project
	}
	b, err := Start(ctx, Spec{Engine: "docker", Image: img, Network: network}, []string{dir}, dir, Limits{PIDs: 256, Memory: "512m", Tmp: "64m"})
	if err != nil {
		if strings.Contains(err.Error(), "could not be pulled") {
			t.Skip("the test image is not available here: ", err)
		}
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })
	return b
}

// runIn runs argv in the box from dir.
func runIn(b *Box, dir string, argv ...string) ([]byte, error) {
	return exec.Command("docker", append([]string{"exec", "--workdir", dir, b.Name}, argv...)...).CombinedOutput()
}

func TestHasLooksInsideTheContainer(t *testing.T) {
	b := startBox(t, t.TempDir(), false)
	if !b.Has(context.Background(), "sh") {
		t.Fatal("sh not found in the container")
	}
	if b.Has(context.Background(), "no-such-program-here") {
		t.Fatal("a missing program was found")
	}
}

// helper runs args in the box through the exec helper, from dir, as the shell tool does.
func helper(b *Box, dir string, args ...string) *exec.Cmd {
	exe, _ := os.Executable()
	w := b.Wrap(exe)
	cmd := exec.Command(w[0], append(w[1:], args...)...)
	cmd.Dir = dir
	return cmd
}

func TestSandboxRunsCommandsInside(t *testing.T) {
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	b := startBox(t, dir, false)
	out, err := helper(b, sub, "/bin/sh", "-c", `pwd; id -u; echo made > made.txt; cat /etc/os-release | grep -c Alpine; `+
		`(wget -q -T 3 -O /dev/null http://1.1.1.1 && echo NET) || echo NONET; echo "home=$HOME"; exit 3`).CombinedOutput()
	if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 3 {
		t.Fatalf("exit status not kept: %v\n%s", err, out)
	}
	s := string(out)
	if !strings.Contains(s, sub+"\n") {
		t.Fatalf("the command did not run in its directory:\n%s", s)
	}
	if !strings.Contains(s, strconv.Itoa(os.Getuid())+"\n") {
		t.Fatalf("not the host user:\n%s", s)
	}
	if !strings.Contains(s, "NONET") || strings.Contains(s, "\nNET") {
		t.Fatalf("the network is reachable:\n%s", s)
	}
	if !strings.Contains(s, "home=/tmp/home") {
		t.Fatalf("HOME is not the container's:\n%s", s)
	}
	if got, err := os.ReadFile(filepath.Join(sub, "made.txt")); err != nil || strings.TrimSpace(string(got)) != "made" {
		t.Fatalf("a file written inside is not on the host: %q %v", got, err)
	}
}

// A command stopped with SIGTERM stops inside the container too: docker exec alone would leave it running.
func TestSandboxStopReachesInside(t *testing.T) {
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	b := startBox(t, dir, false)
	cmd := helper(b, dir, "sh", "-c", "sleep 300")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for !running(b, "sleep 300") {
		if time.Now().After(deadline) {
			t.Fatal("the command never started inside")
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("the helper did not stop")
	}
	for i := 0; i < 50 && running(b, "sleep 300"); i++ {
		time.Sleep(100 * time.Millisecond)
	}
	if running(b, "sleep 300") {
		t.Fatal("the command is still running inside the container")
	}
}

// running reports whether a process whose command line contains s runs in the box.
func running(b *Box, s string) bool {
	out, _ := exec.Command("docker", "exec", b.Name, "ps", "-o", "args").Output()
	for _, l := range strings.Split(string(out), "\n") {
		if strings.Contains(l, s) && !strings.Contains(l, "sh -c") {
			return true
		}
	}
	return false
}

func TestCloseRemovesTheContainer(t *testing.T) {
	dir := t.TempDir()
	b := startBox(t, dir, false)
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("docker", "inspect", b.Name).CombinedOutput(); err == nil {
		t.Fatalf("container %s still exists: %s", b.Name, out)
	}
	if err := b.Close(); err != nil {
		t.Fatalf("a second Close: %v", err)
	}
}

// The home directory, or a directory that holds it, is never mounted: the keys and credentials kept there would be inside.
func TestMountRefusesTheHomeDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if _, err := mountPaths([]string{home}); err == nil {
		t.Fatal("the home directory was mounted")
	}
	if _, err := mountPaths([]string{filepath.Dir(home)}); err == nil {
		t.Fatal("a directory holding the home directory was mounted")
	}
	if runtime.GOOS == "windows" {
		return // a drive path (C:\\...) is never mounted, so there is no project below home to allow
	}
	proj := filepath.Join(home, "src", "app")
	if got, err := mountPaths([]string{proj}); err != nil || len(got) == 0 {
		t.Fatalf("a project below home: %v %v", got, err)
	}
}

// The files of a repository that make the host's git run programs are mounted read-only over the project, when they exist.
func TestRunArgsGuardTheGitDirectory(t *testing.T) {
	dir := t.TempDir()
	for _, p := range []string{".git/hooks", ".git/info"} {
		if err := os.MkdirAll(filepath.Join(dir, p), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, ".git/config"), []byte("[core]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	line := strings.Join(RunArgs("n", "img", false, []string{dir}, dir, DefaultLimits, 1, 1, 1), " ")
	if gd := filepath.Join(dir, ".git"); !strings.Contains(line, "--volume "+gd+":"+gd+" ") {
		t.Errorf("the git directory is not a mount point of its own (a command could rename it away): %s", line)
	}
	for _, g := range []string{"config", "hooks", "info"} {
		p := filepath.Join(dir, ".git", g)
		if !strings.Contains(line, "--volume "+p+":"+p+":ro") {
			t.Errorf("%s is not read-only: %s", p, line)
		}
	}
	if strings.Contains(line, "config.worktree") {
		t.Errorf("a guard that does not exist is mounted (docker would create it as root): %s", line)
	}
}

func TestGitConfigIsReadOnlyInside(t *testing.T) {
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	if err := os.MkdirAll(filepath.Join(dir, ".git", "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", "config"), []byte("[core]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	b := startBox(t, dir, false)
	out, err := helper(b, dir, "sh", "-c", `echo "[core] fsmonitor = evil" >> .git/config && echo WROTE; touch .git/hooks/pre-commit && echo HOOK; mv .git .git.old && echo MOVED; touch src.txt && echo OK`).CombinedOutput()
	s := string(out)
	if strings.Contains(s, "WROTE") || strings.Contains(s, "HOOK") || strings.Contains(s, "MOVED") || !strings.Contains(s, "OK") {
		t.Fatalf("the git guards are writable inside (or the project is not): %v\n%s", err, s)
	}
}

// A linked worktree's .git file names its git directory: it is read-only inside, so a command cannot point the host's git at a
// directory it made.
func TestRunArgsGuardALinkedWorktreesGitFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: /elsewhere/.git/worktrees/w\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	line := strings.Join(RunArgs("n", "img", false, []string{dir}, dir, DefaultLimits, 1, 1, 1), " ")
	if gf := filepath.Join(dir, ".git"); !strings.Contains(line, "--volume "+gf+":"+gf+":ro") {
		t.Errorf("the .git file is writable: %s", line)
	}
}

// A project that is a symbolic link to the root is refused like the root itself.
func TestMountRefusesALinkToTheRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symbolic links to the root are a unix case")
	}
	link := filepath.Join(t.TempDir(), "proj")
	if err := os.Symlink("/", link); err != nil {
		t.Fatal(err)
	}
	if got, err := mountPaths([]string{link}); err == nil {
		t.Fatalf("a link to / was mounted: %v", got)
	}
}

// A git guard that is a link is refused: docker would mount what it names inside the container.
func TestMountRefusesALinkedGitGuard(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symbolic links are a unix case here")
	}
	proj := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(filepath.Join(proj, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(proj, ".git", "hooks")); err != nil {
		t.Fatal(err)
	}
	if got, err := mountPaths([]string{proj}); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("a repository with a linked hooks directory was mounted: %v, %v", got, err)
	}
	wt := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(filepath.Join(wt, ".git", "worktrees", "w"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "key"), filepath.Join(wt, ".git", "worktrees", "w", "config.worktree")); err != nil {
		t.Fatal(err)
	}
	if got, err := mountPaths([]string{wt}); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("a repository with a linked worktree config was mounted: %v, %v", got, err)
	}
}
