package env

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/rl"
)

// posixOnly skips a test that needs a POSIX shell, absolute POSIX paths or a docker script: container tasks run on Linux and macOS hosts.
func posixOnly(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("container tasks need a POSIX host")
	}
}

// pair reports whether argv holds flag immediately followed by value.
func pair(argv []string, flag, value string) bool {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == flag && argv[i+1] == value {
			return true
		}
	}
	return false
}

func mountArgs(argv []string) []string {
	var m []string
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == "--mount" {
			m = append(m, argv[i+1])
		}
	}
	return m
}

func TestContainerArgv(t *testing.T) {
	posixOnly(t)
	tree, home, tmp, cache := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	c := ContainerRun{
		Spec: rl.Container{Image: "docker.io/x/y:1", Workdir: "/repo"}, Docker: "/usr/bin/docker", Tree: tree, Dir: tree, UID: -1, GID: -1,
	}
	env := []string{"HOME=" + home, "TMPDIR=" + tmp, "PATH=/host/bin", "GOMODCACHE=" + cache, "LANG=C"}
	mt := Mounts{RW: []string{tree, home, tmp}}

	argv, err := containerArgv(c, mt, false, env)
	if err != nil {
		t.Fatal(err)
	}
	if argv[0] != "/usr/bin/docker" || argv[1] != "run" || argv[len(argv)-1] != "docker.io/x/y:1" {
		t.Fatalf("a docker run ending with the image was expected: %q", argv)
	}
	for _, want := range [][2]string{{"--network", "none"}, {"--cpus", "4"}, {"--memory", "8g"}, {"-w", tree}, {"--label", containerLabel}, {"--security-opt", "no-new-privileges"}} {
		if !pair(argv, want[0], want[1]) {
			t.Errorf("missing %s %s in %q", want[0], want[1], argv)
		}
	}
	if !slices.Contains(argv, "--pull=never") || !slices.Contains(argv, "--init") || !slices.Contains(argv, "--rm") {
		t.Errorf("a rollout must not pull, and must clean up: %q", argv)
	}
	if slices.Contains(argv, "--user") {
		t.Errorf("a negative uid runs as the image's user: %q", argv)
	}
	// The workspace's own directories, the tree at the workdir and the named cache are mounted, each target once.
	want := []string{
		"type=bind,source=" + tree + ",target=" + tree, "type=bind,source=" + home + ",target=" + home, "type=bind,source=" + tmp + ",target=" + tmp,
		"type=bind,source=" + tree + ",target=/repo", "type=bind,source=" + cache + ",target=" + cache,
	}
	if got := mountArgs(argv); !slices.Equal(got, want) {
		t.Errorf("mounts = %q, want %q", got, want)
	}
	// The container's PATH replaces the host's; the rest of the environment is passed on.
	if !pair(argv, "-e", "PATH="+defaultContainerPath) || pair(argv, "-e", "PATH=/host/bin") {
		t.Errorf("PATH must be the container's: %q", argv)
	}
	if !pair(argv, "-e", "LANG=C") || !pair(argv, "-e", "HOME="+home) {
		t.Errorf("environment not passed on: %q", argv)
	}

	// Network, limits, user and PATH from the task.
	c.Spec.CPUs, c.Spec.Memory, c.Spec.Path, c.UID, c.GID = 1.5, "2g", "/opt/bin:/bin", 1000, 1001
	argv, err = containerArgv(c, mt, true, env)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(argv, "--network") || !pair(argv, "--cpus", "1.5") || !pair(argv, "--memory", "2g") || !pair(argv, "--user", "1000:1001") || !pair(argv, "-e", "PATH=/opt/bin:/bin") {
		t.Errorf("task settings not applied: %q", argv)
	}

	// Read-only mounts, and a workdir equal to the tree mounted once.
	c.Spec.Workdir = tree
	ro := t.TempDir()
	argv, err = containerArgv(c, Mounts{RW: []string{tree}, RO: []string{ro}}, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := mountArgs(argv), []string{"type=bind,source=" + tree + ",target=" + tree, "type=bind,source=" + ro + ",target=" + ro + ",readonly"}; !slices.Equal(got, want) {
		t.Errorf("mounts = %q, want %q", got, want)
	}
}

func TestContainerArgvRefusesWhatItCannotMountSafely(t *testing.T) {
	posixOnly(t)
	tree := t.TempDir()
	base := ContainerRun{Spec: rl.Container{Image: "a"}, Docker: "/usr/bin/docker", Tree: tree, UID: -1, GID: -1}
	cases := map[string]struct {
		mutate func(*ContainerRun, *Mounts)
		want   string
	}{
		"no image":       {func(c *ContainerRun, _ *Mounts) { c.Spec.Image = "" }, "no image"},
		"missing source": {func(_ *ContainerRun, m *Mounts) { m.RW = append(m.RW, filepath.Join(tree, "absent")) }, "not a directory"},
		"comma in path":  {func(_ *ContainerRun, m *Mounts) { m.RW = append(m.RW, "/a,b") }, "cannot mount"},
		"relative path":  {func(_ *ContainerRun, m *Mounts) { m.RW = append(m.RW, "rel") }, "cannot mount"},
		"bad workdir":    {func(c *ContainerRun, _ *Mounts) { c.Spec.Workdir = "/w,x" }, "cannot mount"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c, m := base, Mounts{RW: []string{tree}}
			tc.mutate(&c, &m)
			if argv, err := containerArgv(c, m, false, nil); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %q, %v; want an error containing %q", argv, err, tc.want)
			}
		})
	}
}

// fakeDocker writes a docker that records its arguments (one per line) in log and then runs the command after the image, the way the
// real one runs it in the container: everything from the first "sh" argument on.
func fakeDocker(t *testing.T) (docker, log string) {
	t.Helper()
	dir := t.TempDir()
	log = filepath.Join(dir, "args")
	script := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\" >> " + log + "; done\n" +
		"while [ \"$1\" != sh ] && [ $# -gt 0 ]; do shift; done\n" +
		"[ $# -gt 0 ] || exit 125\n" +
		"exec \"$@\"\n"
	docker = filepath.Join(dir, "docker")
	if err := os.WriteFile(docker, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return docker, log
}

func TestSandboxRunsInTheContainer(t *testing.T) {
	posixOnly(t)
	tree := t.TempDir()
	docker, log := fakeDocker(t)
	s := testSandbox(t)
	pol := ExecPolicy{Mounts: &Mounts{RW: []string{tree}}, Container: &ContainerRun{
		Spec: rl.Container{Image: "img:1", Workdir: "/repo"}, Docker: docker, Tree: tree, UID: -1, GID: -1}}
	res, err := s.Exec(WithExecPolicy(context.Background(), pol), tree, minimalEnv("MARK=7"), `echo "mark=$MARK pwd=$(pwd)"`, 20*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 || !strings.Contains(res.Output, "mark=") {
		t.Fatalf("the command did not run: %+v", res)
	}
	raw, _ := os.ReadFile(log)
	args := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if args[0] != "run" || !pair(args, "--network", "none") || !pair(args, "-w", tree) || !pair(args, "-e", "MARK=7") {
		t.Errorf("docker was not asked for a network-less run in the tree with the environment: %q", args)
	}
	if i := slices.Index(args, "img:1"); i < 0 || args[i+1] != "sh" || args[i+2] != "-c" {
		t.Errorf("the command must follow the image as sh -c: %q", args)
	}

	// A command that cannot be set up is an error, never a run on the host.
	pol.Container.Docker = filepath.Join(tree, "no-docker")
	pol.Mounts = &Mounts{RW: []string{filepath.Join(tree, "absent")}}
	if res, err := s.Exec(WithExecPolicy(context.Background(), pol), tree, minimalEnv(), "echo hi", 20*time.Second); err == nil {
		t.Fatalf("a missing mount source must fail the command, got %+v", res)
	}
}

func TestCommandPrefixOfAContainerTask(t *testing.T) {
	posixOnly(t)
	m, err := NewWorkspaces(WorkspaceOptions{Root: filepath.Join(t.TempDir(), "root"), DisableNetIsolation: true})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	task := goodTask("c1")
	task.Container = &rl.Container{Image: "img:2"}
	root := t.TempDir()
	ws := &Workspace{Dir: root, Root: root, Home: t.TempDir(), Tmp: t.TempDir(), mgr: m, task: task}
	m.containerDocker = "/usr/bin/docker"
	pre := m.CommandPrefix(task, ws)
	if pre[0] != "/usr/bin/docker" || pre[len(pre)-1] != "img:2" || !pair(pre, "--network", "none") {
		t.Fatalf("prefix = %q", pre)
	}
	// A prefix that cannot be built stops every command with a message instead of running on the host.
	ws.Home = filepath.Join(root, "absent")
	pre = m.CommandPrefix(task, ws)
	if len(pre) < 3 || pre[0] != "sh" || !strings.Contains(pre[2], "cannot start the task") || !strings.Contains(pre[2], "exit 125") {
		t.Fatalf("prefix = %q", pre)
	}
	// Without a container nothing changes.
	task.Container = nil
	if pre := m.CommandPrefix(task, ws); len(pre) > 0 && pre[len(pre)-1] == "img:2" {
		t.Fatalf("prefix = %q", pre)
	}
}

func TestContainerKeyOnlyChangesContainerTasks(t *testing.T) {
	task := goodTask("k")
	if containerKey(task) != "" {
		t.Fatal("a task without a container must keep its snapshot key")
	}
	a, b := task, task
	a.Container = &rl.Container{Image: "x"}
	b.Container = &rl.Container{Image: "x", Workdir: "/w"}
	if containerKey(a) == "" || containerKey(a) == containerKey(b) {
		t.Fatalf("keys: %q %q", containerKey(a), containerKey(b))
	}
}

func TestContainerTaskNeedsDockerAndNoBwrap(t *testing.T) {
	m, err := NewWorkspaces(WorkspaceOptions{Root: filepath.Join(t.TempDir(), "root"), DisableNetIsolation: true})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	task := goodTask("c2")
	task.Container = &rl.Container{Image: "img"}
	// Docker is looked up on the harness's PATH: with none there the task is skipped like one that needs a missing tool.
	t.Setenv("PATH", t.TempDir())
	if got := m.MissingTools(task); !slices.Equal(got, []string{"docker"}) {
		t.Fatalf("MissingTools = %q", got)
	}
	if got := m.MissingTools(goodTask("plain")); len(got) != 0 {
		t.Fatalf("a task without a container does not need docker: %q", got)
	}
	if why := m.ContainerConflict(task); why != "" {
		t.Fatalf("no confinement here, so no conflict: %q", why)
	}
	if err := m.checkContainer(context.Background(), task); err == nil || !IsInfra(err) || !strings.Contains(err.Error(), "needs docker") {
		t.Fatalf("checkContainer = %v", err)
	}
}

func TestCheckContainerRejectsAnImageThatIsNotThere(t *testing.T) {
	posixOnly(t)
	m, err := NewWorkspaces(WorkspaceOptions{Root: filepath.Join(t.TempDir(), "root"), DisableNetIsolation: true})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	// A docker that knows no image: the check names the image and the fix, and it is an infrastructure failure, not the model's.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte("#!/bin/sh\necho 'Error: No such image' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	task := goodTask("c3")
	task.Container = &rl.Container{Image: "img:missing"}
	err = m.checkContainer(context.Background(), task)
	if err == nil || !IsInfra(err) || !strings.Contains(err.Error(), "img:missing") || !strings.Contains(err.Error(), "docker pull") {
		t.Fatalf("checkContainer = %v", err)
	}
	if err := m.checkContainer(context.Background(), goodTask("plain")); err != nil {
		t.Fatalf("a task without a container needs no check: %v", err)
	}
}

func TestContainerTaskIsSkippedUnderBwrap(t *testing.T) {
	m, err := NewWorkspaces(WorkspaceOptions{Root: filepath.Join(t.TempDir(), "root"), DisableNetIsolation: true, Confine: ConfineBwrap})
	if errors.Is(err, ErrConfinementUnavailable) {
		t.Skipf("bubblewrap unavailable: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	task := goodTask("c4")
	if why := m.ContainerConflict(task); why != "" {
		t.Fatalf("a task without a container has no conflict: %q", why)
	}
	task.Container = &rl.Container{Image: "img"}
	if why := m.ContainerConflict(task); !strings.Contains(why, "--confine bwrap") {
		t.Fatalf("conflict = %q", why)
	}
}

func TestImageCheckRunsInTheEnvironmentOfTheTaskCommands(t *testing.T) {
	posixOnly(t)
	seen := filepath.Join(t.TempDir(), "seen")
	dir := t.TempDir()
	script := "#!/bin/sh\nprintf '%s' \"$DOCKER_CONTEXT\" > " + seen + "\nexit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	t.Setenv("DOCKER_CONTEXT", "remote-daemon")
	task := goodTask("c5")
	task.Container = &rl.Container{Image: "img"}
	check := func(passEnv []string) string {
		m, err := NewWorkspaces(WorkspaceOptions{Root: filepath.Join(t.TempDir(), "root"), DisableNetIsolation: true, PassEnv: passEnv})
		if err != nil {
			t.Fatal(err)
		}
		defer m.Close()
		_ = os.Remove(seen)
		if err := m.checkContainer(context.Background(), task); err != nil {
			t.Fatal(err)
		}
		b, _ := os.ReadFile(seen)
		return string(b)
	}
	// The harness's own daemon variables do not reach the check, as they do not reach the commands, unless the operator passes them:
	// a check that succeeds where the commands cannot connect would turn an infrastructure failure into a failed episode.
	if got := check(nil); got != "" {
		t.Errorf("the check saw DOCKER_CONTEXT=%q that the commands will not have", got)
	}
	if got := check([]string{"DOCKER_CONTEXT"}); got != "remote-daemon" {
		t.Errorf("a passed DOCKER_CONTEXT must reach the check, got %q", got)
	}
}

func TestSavepointWorkspacesCheckTheContainerToo(t *testing.T) {
	m, err := NewWorkspaces(WorkspaceOptions{Root: filepath.Join(t.TempDir(), "root"), DisableNetIsolation: true})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	t.Setenv("PATH", t.TempDir())
	task := goodTask("c6")
	task.Container = &rl.Container{Image: "img"}
	if _, err := m.PrepareID(context.Background(), task, "sp1"); err == nil || !IsInfra(err) || !strings.Contains(err.Error(), "needs docker") {
		t.Fatalf("PrepareID = %v", err)
	}
	if _, err := m.RestoreWorkspace(context.Background(), task, WorkspaceInfo{ID: "sp1"}, t.TempDir(), t.TempDir()); err == nil || !IsInfra(err) || !strings.Contains(err.Error(), "needs docker") {
		t.Fatalf("RestoreWorkspace = %v", err)
	}
}
