package env

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/anemos-labs/sleipnir/internal/rl"
)

// Containers. A task with rl.Task.Container runs its agent commands and its
// setup through `docker run` in the task's image: the toolchain and the
// dependencies are where the image put them, the workspace's tree is mounted in,
// and nothing else of the host is (the blob store, the run directory and other
// workspaces stay out of sight because they are never mounted, the verifier's
// hidden files because they are written only into the verification checkout).
// The container is the boundary, so these commands are not wrapped in the
// bubblewrap confinement as well. The verifier is a host command (see
// rl.Container).

const (
	// defaultContainerPath is the PATH of a container whose task names none: the usual homes of the conda environments, Go and Cargo images
	// ship, then the system directories.
	defaultContainerPath = "/opt/miniconda3/envs/testbed/bin:/opt/miniconda3/bin:/opt/conda/envs/testbed/bin:/opt/conda/bin:" +
		"/usr/local/cargo/bin:/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	defaultContainerCPUs   = 4
	defaultContainerMemory = "8g"
	// containerPids bounds the processes of one command: a fork bomb in a test run must not take the host's process table.
	containerPids = 4096
	// containerLabel marks every container this package starts, so stragglers can be listed with `docker ps --filter label=...`.
	containerLabel = "sleipnir.rl=1"
)

// ContainerRun says how one command runs in a container. It travels in
// ExecPolicy for the commands the Sandbox runs, and builds the argv prefix of
// the agent's shell.
type ContainerRun struct {
	// Spec is the task's container.
	Spec rl.Container
	// Docker is the docker binary ("" looks `docker` up on the harness's PATH).
	Docker string
	// Tree is the tree root mounted at Spec.Workdir.
	Tree string
	// Dir is the working directory in the container; it is also a path on the
	// host, since the tree is mounted at its own path ("" is the command's dir).
	Dir string
	// UID and GID run the command as that user, so the files it makes in the
	// workspace belong to the harness's user; negative runs it as the image's user.
	UID, GID int
}

// hostUser returns the uid and gid containers run as: the harness's own unless it is root (the image's user, root
// too, then makes files the harness's user owns) or the platform has no such ids.
func hostUser() (uid, gid int) {
	if uid, gid = os.Getuid(), os.Getgid(); uid <= 0 || gid < 0 {
		return -1, -1
	}
	return uid, gid
}

// containerRun is the ContainerRun of task's commands in tree, or nil when the task has no container.
func (m *Workspaces) containerRun(task rl.Task, tree, dir string) *ContainerRun {
	if task.Container == nil {
		return nil
	}
	uid, gid := hostUser()
	return &ContainerRun{Spec: *task.Container, Docker: m.containerDocker, Tree: tree, Dir: dir, UID: uid, GID: gid}
}

// bindable rejects a path docker's --mount syntax would split or misread.
func bindable(p string) error {
	if p == "" || !strings.HasPrefix(p, "/") || strings.ContainsAny(p, ",\"'\n\r\x00") {
		return fmt.Errorf("cannot mount %q into a container", p)
	}
	return nil
}

// containerArgv returns the command line that starts c's image and ends with the image reference: what follows it is the command to run in the
// container. env is the command's environment (its PATH is replaced by the container's, everything else is passed on: it is the scrubbed
// environment of the workspace, no credentials). The mounts are mt's directories (writable and read-only), the tree at its own path and at
// Spec.Workdir, and the caches the environment names that exist. A missing mount source is an error, never an empty directory made as root.
func containerArgv(c ContainerRun, mt Mounts, network bool, env []string) ([]string, error) {
	docker := c.Docker
	if docker == "" {
		p, err := exec.LookPath("docker")
		if err != nil {
			return nil, fmt.Errorf("docker is not available: %w", err)
		}
		docker = p
	}
	if c.Spec.Image == "" {
		return nil, fmt.Errorf("the task's container names no image")
	}
	cpus, mem := c.Spec.CPUs, c.Spec.Memory
	if cpus <= 0 {
		cpus = defaultContainerCPUs
	}
	if mem == "" {
		mem = defaultContainerMemory
	}
	a := []string{docker, "run", "--rm", "-i", "--init", "--pull=never", "--security-opt", "no-new-privileges", "--label", containerLabel,
		"--cpus", strconv.FormatFloat(cpus, 'f', -1, 64), "--memory", mem, "--pids-limit", strconv.Itoa(containerPids)}
	if !network {
		a = append(a, "--network", "none")
	}
	if c.UID >= 0 && c.GID >= 0 {
		a = append(a, "--user", fmt.Sprintf("%d:%d", c.UID, c.GID))
	}
	targets := map[string]bool{}
	var firstErr error
	mount := func(src, dst string, ro bool) {
		if targets[dst] || firstErr != nil {
			return
		}
		for _, p := range []string{src, dst} {
			if err := bindable(p); err != nil {
				firstErr = err
				return
			}
		}
		if !dirExists(src) {
			firstErr = fmt.Errorf("cannot mount %s into the container: it is not a directory", src)
			return
		}
		targets[dst] = true
		spec := "type=bind,source=" + src + ",target=" + dst
		if ro {
			spec += ",readonly"
		}
		a = append(a, "--mount", spec)
	}
	for _, p := range mt.RW {
		mount(p, p, false)
	}
	for _, p := range mt.RO {
		mount(p, p, true)
	}
	if c.Tree != "" {
		mount(c.Tree, c.Tree, false)
		if c.Spec.Workdir != "" {
			mount(c.Tree, c.Spec.Workdir, false)
		}
	}
	// A cache the environment names is shared with the host's other commands, as the confinement shares it.
	for _, kv := range env {
		name, val, ok := strings.Cut(kv, "=")
		if ok && val != "" && strings.HasPrefix(val, "/") && containsString(cacheRWEnv, name) && dirExists(val) {
			mount(val, val, false)
		}
	}
	if firstErr != nil {
		return nil, firstErr
	}
	if c.Dir != "" {
		a = append(a, "-w", c.Dir)
	}
	path := c.Spec.Path
	if path == "" {
		path = defaultContainerPath
	}
	for _, kv := range env {
		if name, _, ok := strings.Cut(kv, "="); ok && name != "" && name != "PATH" {
			a = append(a, "-e", kv)
		}
	}
	a = append(a, "-e", "PATH="+path, c.Spec.Image)
	return a, nil
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// failingPrefix is the shell prefix used when the container's command line cannot be built: every command then says why on stderr and
// exits 125, instead of running on the host.
func failingPrefix(err error) []string {
	msg := "sleipnir: cannot start the task's container: " + err.Error()
	return []string{"sh", "-c", "printf '%s\\n' " + shellQuote(msg) + " >&2; exit 125", "sleipnir-container"}
}

// checkContainer checks that docker answers and holds the task's image; a rollout never pulls, so an image that is not
// there is an infrastructure failure that says how to fix it.
func (m *Workspaces) checkContainer(ctx context.Context, task rl.Task) error {
	if task.Container == nil {
		return nil
	}
	docker, err := exec.LookPath("docker")
	if err != nil {
		return Infraf("container", "task %s needs docker, which is not on the PATH", task.ID)
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, docker, "image", "inspect", "--format", "{{.Id}}", task.Container.Image)
	cmd.Env = dockerEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		return Infraf("container", "image %s of task %s is not available here (a rollout does not pull: `docker pull` it first): %v: %s", task.Container.Image, task.ID, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// dockerEnv is the environment the docker client of a probe runs in: a minimal PATH and the variables that say which daemon it talks to.
func dockerEnv() []string {
	env := []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C"}
	for _, name := range []string{"DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_CONFIG", "XDG_RUNTIME_DIR", "HOME"} {
		if v := os.Getenv(name); v != "" {
			env = append(env, name+"="+v)
		}
	}
	return env
}

// containerKey is what of a task's container the cached snapshot depends on: setup runs in the image, so another image, workdir or PATH
// is another snapshot. It is "" for a task without a container.
func containerKey(t rl.Task) string {
	if t.Container == nil {
		return ""
	}
	return "container\x00" + t.Container.Image + "\x00" + t.Container.Workdir + "\x00" + t.Container.Path
}

// containerConflict says why a container task cannot run under the confinement this run uses ("" when it can): the container confines the
// agent's commands, and a verifier that starts docker itself (as one for an image task does) cannot do so inside bubblewrap.
func (m *Workspaces) containerConflict(t rl.Task) string {
	if t.Container != nil && m.Confined() {
		return "a container task runs without --confine bwrap: the container confines the agent's commands, and the verifier needs docker"
	}
	return ""
}
