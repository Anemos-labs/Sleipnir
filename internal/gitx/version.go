package gitx

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// MinGitVersion is the oldest git gitx supports. 2.31 introduced
// GIT_CONFIG_COUNT/KEY/VALUE, which is how attribute-selected drivers are
// neutralized without any quoting ambiguity in driver names (see guard.go), and
// `rev-parse --path-format`.
const MinGitVersion = "2.31"

var versionRE = regexp.MustCompile(`git version (\d+)\.(\d+)`)

var versionCache sync.Map // path -> error (nil when acceptable)

// checkVersion runs `git --version` once per binary path.
func checkVersion(git string) error {
	if v, ok := versionCache.Load(git); ok {
		if v == nil {
			return nil
		}
		return v.(error)
	}
	err := probeVersion(git)
	if err == nil {
		versionCache.Store(git, nil)
	} else {
		versionCache.Store(git, err)
	}
	return err
}

func probeVersion(git string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, git, "--version")
	cmd.Env = []string{"LC_ALL=C", "PATH=" + os.Getenv("PATH")}
	out, err := cmd.Output()
	if err != nil {
		return &Error{Kind: KindNoGit, Op: "version", ExitCode: -1, Detail: "cannot run " + git, Err: err}
	}
	m := versionRE.FindStringSubmatch(string(out))
	if m == nil {
		return &Error{Kind: KindNoGit, Op: "version", ExitCode: -1, Detail: "unrecognised version output: " + strings.TrimSpace(string(out))}
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	if major < 2 || (major == 2 && minor < 31) {
		return &Error{Kind: KindNoGit, Op: "version", ExitCode: -1,
			Detail: fmt.Sprintf("git %d.%d is too old; gitx needs >= %s", major, minor, MinGitVersion)}
	}
	return nil
}
