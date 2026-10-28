// Package sandbox runs a session's shell commands inside a container instead of on the host.
//
// The harness stays outside: the agents, the model's keys and the session's files are on the host, and each command is sent
// into one long-lived container per session (docker exec). The project is mounted at the same path inside it, so paths,
// working directories and the files agents edit are the same on both sides, while what a command runs (a build, a test, a
// script from an untrusted repository) is confined to the container: no network unless asked for, every capability dropped,
// no privilege escalation, bounded processes and memory, a non-root user, and none of the host's environment.
package sandbox

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// DefaultImage is the image a sandbox runs when none is named: a Debian with git, curl, a C toolchain and the common
// libraries builds expect.
const DefaultImage = "buildpack-deps:bookworm"

// Label marks every container a sandbox starts, so that one left by a process that died can be found and removed.
const Label = "io.sleipnir.sandbox"

// Spec is what a session's sandbox is: the container engine (only docker), its image, and whether its commands may use the
// network. The zero Spec is no sandbox.
type Spec struct {
	Engine  string `json:"engine,omitempty"`
	Image   string `json:"image,omitempty"`
	Network bool   `json:"network,omitempty"`
}

// Enabled reports whether the spec asks for a sandbox.
func (s Spec) Enabled() bool { return s.Engine != "" }

// String is the spec as --sandbox takes it: docker, or docker=IMAGE when the image is not the default.
func (s Spec) String() string {
	if !s.Enabled() {
		return ""
	}
	if s.Image == "" || s.Image == DefaultImage {
		return s.Engine
	}
	return s.Engine + "=" + s.Image
}

// Parse reads a --sandbox value: "" or "none" is no sandbox, "docker" a container of DefaultImage, "docker=IMAGE" one of
// IMAGE. The network stays off; the caller sets Network from --sandbox-network.
func Parse(v string) (Spec, error) {
	v = strings.TrimSpace(v)
	if v == "" || v == "none" || v == "off" {
		return Spec{}, nil
	}
	engine, image, _ := strings.Cut(v, "=")
	if engine != "docker" {
		return Spec{}, fmt.Errorf("--sandbox: %q is not a sandbox: docker, or docker=IMAGE", v)
	}
	image = strings.TrimSpace(image)
	if image == "" {
		image = DefaultImage
	}
	if err := checkImage(image); err != nil {
		return Spec{}, err
	}
	return Spec{Engine: engine, Image: image}, nil
}

// checkImage refuses an image reference that could be read as an option or that has characters no reference has.
func checkImage(image string) error {
	if strings.HasPrefix(image, "-") || len(image) > 255 {
		return fmt.Errorf("--sandbox: %q is not an image name", image)
	}
	for _, r := range image {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._-/:@+", r)
		if !ok {
			return fmt.Errorf("--sandbox: %q is not an image name", image)
		}
	}
	return nil
}

// Limits bound what the commands of one sandbox may use together.
type Limits struct {
	// PIDs is the most processes the container may have (a fork bomb stops there).
	PIDs int
	// Memory is the container's memory ceiling, as docker takes it ("8g").
	Memory string
	// Tmp is the size of the in-memory /tmp ("4g").
	Tmp string
}

// DefaultLimits are the limits a sandbox gets when the caller sets none.
var DefaultLimits = Limits{PIDs: 2048, Memory: "8g", Tmp: "4g"}

// Box is a running sandbox: one container that a session's commands are sent into.
type Box struct {
	// Name is the container's name.
	Name string
	// Spec is what the box was started from.
	Spec Spec
	// Mounts are the host directories mounted at the same path inside.
	Mounts []string
	docker string
}

// Available reports whether a container engine answers: the docker command is on PATH and its daemon replies.
func Available(ctx context.Context) error {
	bin, err := exec.LookPath("docker")
	if err != nil {
		return errors.New("the docker command is not installed (a sandbox needs Docker or a compatible engine with the docker command)")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "version", "--format", "{{.Server.Version}}").CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker does not answer: %s", firstLine(out, err))
	}
	return nil
}

// Start starts the sandbox of a session: it pulls the image when it is missing, then runs a container that does nothing but
// wait, with each of mounts bound at its own path (read-write) and workdir as its working directory. The caller sends commands
// in through Wrap and removes the container with Close.
func Start(ctx context.Context, spec Spec, mounts []string, workdir string, lim Limits) (*Box, error) {
	if !spec.Enabled() {
		return nil, errors.New("no sandbox was asked for")
	}
	if spec.Engine != "docker" {
		return nil, fmt.Errorf("unknown sandbox engine %q", spec.Engine)
	}
	if runtime.GOOS == "windows" {
		return nil, errors.New("a sandbox mounts the project at the same path inside the container, which needs a Unix host")
	}
	if err := Available(ctx); err != nil {
		return nil, err
	}
	bin, _ := exec.LookPath("docker")
	image := spec.Image
	if image == "" {
		image = DefaultImage
	}
	if err := ensureImage(ctx, bin, image); err != nil {
		return nil, err
	}
	if lim.PIDs <= 0 {
		lim.PIDs = DefaultLimits.PIDs
	}
	if lim.Memory == "" {
		lim.Memory = DefaultLimits.Memory
	}
	if lim.Tmp == "" {
		lim.Tmp = DefaultLimits.Tmp
	}
	paths, err := mountPaths(mounts)
	if err != nil {
		return nil, err
	}
	name := "sleipnir-sbx-" + randomHex(6)
	args := RunArgs(name, image, spec.Network, paths, workdir, lim, os.Getuid(), os.Getgid(), os.Getpid())
	rctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if out, err := exec.CommandContext(rctx, bin, args...).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("the sandbox did not start: %s", firstLine(out, err))
	}
	b := &Box{Name: name, Spec: Spec{Engine: spec.Engine, Image: image, Network: spec.Network}, Mounts: paths, docker: bin}
	// HOME is on the in-memory /tmp: tools that write caches there (go, npm, pip) find it, and nothing of the host's home is inside.
	if out, err := exec.CommandContext(rctx, bin, "exec", name, "mkdir", "-p", "/tmp/home").CombinedOutput(); err != nil {
		_ = b.Close()
		return nil, fmt.Errorf("the sandbox did not start: %s", firstLine(out, err))
	}
	return b, nil
}

// Has reports whether prog is on the container's PATH.
func (b *Box) Has(ctx context.Context, prog string) bool {
	return exec.CommandContext(ctx, b.docker, "exec", b.Name, "sh", "-c", `command -v "$1" >/dev/null`, "sh", prog).Run() == nil
}

// RunArgs is the docker command line (after "docker") that starts a sandbox container: detached, removed when it stops,
// labelled with the process that owns it, with an init that reaps orphans, no network unless network is set, every capability
// dropped, no privilege escalation, the limits, the host user's ids, an in-memory /tmp and HOME, and each path bound at itself.
// The entrypoint is replaced so that an image's own start script does not run.
func RunArgs(name, image string, network bool, paths []string, workdir string, lim Limits, uid, gid, owner int) []string {
	net := "none"
	if network {
		net = "bridge"
	}
	host, _ := os.Hostname()
	args := []string{"run", "--detach", "--rm", "--name", name,
		"--label", Label + "=1", "--label", Label + ".owner=" + strconv.Itoa(owner), "--label", Label + ".host=" + host,
		"--init", "--network", net, "--cap-drop", "ALL", "--security-opt", "no-new-privileges",
		"--pids-limit", strconv.Itoa(lim.PIDs), "--memory", lim.Memory,
		"--tmpfs", "/tmp:rw,exec,nosuid,size=" + lim.Tmp,
		"--env", "HOME=/tmp/home", "--env", "TMPDIR=/tmp",
	}
	if uid >= 0 {
		args = append(args, "--user", strconv.Itoa(uid)+":"+strconv.Itoa(gid))
	}
	for _, p := range paths {
		args = append(args, "--volume", p+":"+p)
	}
	if workdir != "" {
		args = append(args, "--workdir", workdir)
	}
	return append(args, "--entrypoint", "sleep", image, "infinity")
}

// mountPaths is every directory to bind: each mount and, when it differs, the path its symlinks resolve to, so that a working
// directory read either way exists inside. A directory that does not exist is made first (docker would make it as root).
func mountPaths(mounts []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	add := func(p string) {
		if p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, m := range mounts {
		if m == "" {
			continue
		}
		abs, err := filepath.Abs(m)
		if err != nil {
			return nil, err
		}
		if abs == "/" || strings.ContainsAny(abs, ":,") {
			return nil, fmt.Errorf("the sandbox cannot mount %s", abs)
		}
		if err := os.MkdirAll(abs, 0o755); err != nil {
			return nil, err
		}
		add(filepath.Clean(abs))
		if real, err := filepath.EvalSymlinks(abs); err == nil {
			add(real)
		}
	}
	return out, nil
}

// ensureImage pulls image when the engine does not have it yet.
func ensureImage(ctx context.Context, bin, image string) error {
	ictx, cancel := context.WithTimeout(ctx, 30*time.Second)
	err := exec.CommandContext(ictx, bin, "image", "inspect", "--format", "{{.Id}}", image).Run()
	cancel()
	if err == nil {
		return nil
	}
	pctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	if out, err := exec.CommandContext(pctx, bin, "pull", "--quiet", image).CombinedOutput(); err != nil {
		return fmt.Errorf("the sandbox image %s could not be pulled: %s", image, firstLine(out, err))
	}
	return nil
}

// Wrap is the argument prefix a shell command runs under to run in the box: exe (this program) re-entered as the exec helper
// (ExecMain), which sends the command into the container from the directory it was started in.
func (b *Box) Wrap(exe string) []string {
	return []string{exe, ExecCommand, b.Name, "--"}
}

// Close removes the container and everything running in it.
func (b *Box) Close() error {
	if b == nil || b.Name == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, b.docker, "rm", "--force", b.Name).CombinedOutput()
	if err != nil && !bytes.Contains(out, []byte("No such container")) {
		return fmt.Errorf("removing sandbox %s: %s", b.Name, firstLine(out, err))
	}
	return nil
}

// Prune removes the sandboxes of this host whose owning process is gone: a session that crashed leaves its container running,
// and the next one to start cleans it up. Errors are ignored: pruning is best effort.
func Prune(ctx context.Context) {
	bin, err := exec.LookPath("docker")
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	host, _ := os.Hostname()
	out, err := exec.CommandContext(ctx, bin, "ps", "--all", "--filter", "label="+Label+"=1", "--filter", "label="+Label+".host="+host,
		"--format", `{{.Names}} {{.Label "`+Label+`.owner"}}`).Output()
	if err != nil {
		return
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		name, owner, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok || !strings.HasPrefix(name, "sleipnir-sbx-") {
			continue
		}
		pid, err := strconv.Atoi(owner)
		if err != nil || pid <= 0 || processAlive(pid) {
			continue
		}
		_ = exec.CommandContext(ctx, bin, "rm", "--force", name).Run()
	}
}

// randomHex is n random bytes in hex.
func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(b)
}

// firstLine is the first non-empty line of a command's output, or the error when it printed nothing.
func firstLine(out []byte, err error) string {
	for _, l := range strings.Split(string(out), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			if len(l) > 300 {
				l = l[:300]
			}
			return l
		}
	}
	if err != nil {
		return err.Error()
	}
	return "no output"
}
