// Package update tells a person that a newer Sleipnir is out, and installs it: `sleipnir update`. It asks GitHub for the latest release of
// the repository (once a day at most, in the background, and never in the way: the notice at the start of the chat is read from what the
// last check kept), and installing is what scripts/install.sh does: download the archive of this machine from the release, check its
// SHA-256 against the release's checksums.txt, and put the binary where the running one is.
package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Repo is the repository the releases are of.
const Repo = "anemos-labs/sleipnir"

// checkEvery is how long a check is good for.
const checkEvery = 24 * time.Hour

// Options say where to ask and where to keep the answer; the zero value is the real thing.
type Options struct {
	API       string       // https://api.github.com
	Repo      string       // Repo
	HTTP      *http.Client // with a timeout of its own
	CachePath string       // where the last check is kept ("": not kept)
	Now       func() time.Time
	GOOS      string
	GOARCH    string
	Agent     string // the User-Agent
}

// api uses a trimmed API override or the public GitHub API endpoint.
func (o Options) api() string {
	if o.API != "" {
		return strings.TrimRight(o.API, "/")
	}
	return "https://api.github.com"
}

// repo selects the configured update repository or the built-in repository.
func (o Options) repo() string {
	if o.Repo != "" {
		return o.Repo
	}
	return Repo
}

// client uses an injected HTTP client or a new client with a twenty-second timeout.
func (o Options) client() *http.Client {
	if o.HTTP != nil {
		return o.HTTP
	}
	return &http.Client{Timeout: 20 * time.Second}
}

// now uses an injected update clock or the system clock.
func (o Options) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

// goos selects an explicit release platform or the running OS.
func (o Options) goos() string {
	if o.GOOS != "" {
		return o.GOOS
	}
	return runtime.GOOS
}

// goarch selects an explicit release architecture or the running architecture.
func (o Options) goarch() string {
	if o.GOARCH != "" {
		return o.GOARCH
	}
	return runtime.GOARCH
}

// Release is a published release.
type Release struct {
	Tag    string  `json:"tag_name"`
	URL    string  `json:"html_url"`
	Assets []Asset `json:"assets"`
}

// Asset is a file of a release.
type Asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

// Status is what a check found.
type Status struct {
	Checked time.Time `json:"checked"`
	Latest  string    `json:"latest"` // the tag of the latest release
	URL     string    `json:"url"`
	Behind  int       `json:"behind"` // commits the release is ahead of this build; -1 when it could not be told
}

func (o Options) get(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	agent := o.Agent
	if agent == "" {
		agent = "sleipnir-update"
	}
	req.Header.Set("User-Agent", agent)
	resp, err := o.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s answered http %d", req.URL.Host, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, errors.New("the answer is larger than expected")
	}
	return b, nil
}

// Latest is the latest published release.
func Latest(ctx context.Context, o Options) (Release, error) {
	b, err := o.get(ctx, o.api()+"/repos/"+o.repo()+"/releases/latest", 4<<20)
	if err != nil {
		return Release{}, err
	}
	var r Release
	if err := json.Unmarshal(b, &r); err != nil || r.Tag == "" {
		return Release{}, errors.New("the release answer is not a release")
	}
	return r, nil
}

// behind is how many commits tag is ahead of commit, or -1.
func behind(ctx context.Context, o Options, commit, tag string) int {
	if commit == "" || commit == "none" {
		return -1
	}
	b, err := o.get(ctx, o.api()+"/repos/"+o.repo()+"/compare/"+commit+"..."+tag, 8<<20)
	if err != nil {
		return -1
	}
	var c struct {
		AheadBy int `json:"ahead_by"`
	}
	if json.Unmarshal(b, &c) != nil {
		return -1
	}
	return c.AheadBy
}

// Check asks GitHub: what is the latest release, and how far ahead of this build (its commit) is it.
func Check(ctx context.Context, o Options, version, commit string) (Status, error) {
	_, st, err := Fetch(ctx, o, version, commit)
	return st, err
}

// Fetch is Check with the release itself, which Install needs.
func Fetch(ctx context.Context, o Options, version, commit string) (Release, Status, error) {
	rel, err := Latest(ctx, o)
	if err != nil {
		return Release{}, Status{}, err
	}
	st := Status{Checked: o.now(), Latest: rel.Tag, URL: rel.URL, Behind: -1}
	if Newer(version, rel.Tag) {
		st.Behind = behind(ctx, o, commit, rel.Tag)
	}
	return rel, st, nil
}

// Newer reports whether tag (v1.2.3) is a later version than version (1.2.3, as the build says it). A version that is not numbers ("dev",
// a build from source) is never behind.
func Newer(version, tag string) bool {
	a, ok1 := parseVersion(version)
	b, ok2 := parseVersion(tag)
	if !ok1 || !ok2 {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return b[i] > a[i]
		}
	}
	return false
}

func parseVersion(s string) (v [3]int, ok bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return v, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return v, false
		}
		v[i] = n
	}
	return v, true
}

// Notice is the line the chat says at its start when the last check found a newer release, and "" when it did not (or there was none).
func Notice(version string, st Status) string {
	if st.Latest == "" || !Newer(version, st.Latest) {
		return ""
	}
	if st.Behind > 0 {
		return fmt.Sprintf("A newer Sleipnir is out: %s, %d commits ahead of yours. Run `sleipnir update`.", st.Latest, st.Behind)
	}
	return fmt.Sprintf("A newer Sleipnir is out: %s. Run `sleipnir update`.", st.Latest)
}

// Cached is what the last check kept.
func Cached(o Options) (Status, bool) {
	if o.CachePath == "" {
		return Status{}, false
	}
	b, err := os.ReadFile(o.CachePath)
	if err != nil {
		return Status{}, false
	}
	var st Status
	if json.Unmarshal(b, &st) != nil {
		return Status{}, false
	}
	return st, true
}

// Remember keeps a check, for the start of the next chat to read.
func Remember(o Options, st Status) {
	if o.CachePath == "" {
		return
	}
	b, err := json.Marshal(st)
	if err != nil || os.MkdirAll(filepath.Dir(o.CachePath), 0o700) != nil {
		return
	}
	tmp := o.CachePath + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, o.CachePath)
	}
}

// Refresh checks again when the last check is more than a day old, and keeps the answer for the next start. It is meant for a goroutine:
// every failure is silent (no network is not news), and a failed check is kept as a check, so that it is not tried at every start.
func Refresh(ctx context.Context, o Options, version, commit string) {
	old, had := Cached(o)
	if had && o.now().Sub(old.Checked) < checkEvery {
		return
	}
	st, err := Check(ctx, o, version, commit)
	if err != nil {
		old.Checked = o.now()
		Remember(o, old)
		return
	}
	Remember(o, st)
}

// ArchiveName is the file of a release for this machine, as .goreleaser.yaml names it.
func ArchiveName(o Options, tag string) string {
	ext := ".tar.gz"
	if o.goos() == "windows" {
		ext = ".zip"
	}
	return "sleipnir_" + strings.TrimPrefix(tag, "v") + "_" + o.goos() + "_" + o.goarch() + ext
}

// Install downloads the release's archive for this machine, checks it against the release's checksums.txt and puts the binary it holds in
// place of exe (the running one). Where the running binary cannot be overwritten (Windows) it is renamed out of the way first.
func Install(ctx context.Context, o Options, rel Release, exe string) error {
	name := ArchiveName(o, rel.Tag)
	var archive, sums string
	for _, a := range rel.Assets {
		switch a.Name {
		case name:
			archive = a.URL
		case "checksums.txt":
			sums = a.URL
		}
	}
	if archive == "" || sums == "" {
		return fmt.Errorf("release %s has no %s for %s/%s (see %s)", rel.Tag, name, o.goos(), o.goarch(), rel.URL)
	}
	data, err := o.get(ctx, archive, 300<<20)
	if err != nil {
		return fmt.Errorf("downloading %s: %w", name, err)
	}
	sumFile, err := o.get(ctx, sums, 1<<20)
	if err != nil {
		return fmt.Errorf("downloading checksums.txt: %w", err)
	}
	want := checksumOf(string(sumFile), name)
	got := sha256.Sum256(data)
	if want == "" || !strings.EqualFold(want, hex.EncodeToString(got[:])) {
		return fmt.Errorf("the checksum of %s is not the one in checksums.txt: nothing was installed", name)
	}
	bin := "sleipnir"
	if o.goos() == "windows" {
		bin += ".exe"
	}
	exeBytes, err := extract(data, name, bin)
	if err != nil {
		return err
	}
	return replace(exe, exeBytes)
}

// checksumOf finds a named checksum entry, accepting an optional binary-file asterisk marker.
func checksumOf(sums, name string) string {
	for _, line := range strings.Split(sums, "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name {
			return f[0]
		}
	}
	return ""
}

// extract is the file bin of a .tar.gz or .zip archive.
func extract(data []byte, name, bin string) ([]byte, error) {
	const limit = 300 << 20
	if strings.HasSuffix(name, ".zip") {
		zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return nil, fmt.Errorf("%s is not a zip file", name)
		}
		for _, f := range zr.File {
			if path.Base(f.Name) == bin {
				rc, err := f.Open()
				if err != nil {
					return nil, err
				}
				defer rc.Close()
				return io.ReadAll(io.LimitReader(rc, limit))
			}
		}
		return nil, fmt.Errorf("%s holds no %s", name, bin)
	}
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%s is not a gzip file", name)
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil, fmt.Errorf("%s holds no %s", name, bin)
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag == tar.TypeReg && path.Base(h.Name) == bin {
			return io.ReadAll(io.LimitReader(tr, limit))
		}
	}
}

// replace puts content in place of the file exe, whole or not at all: it is written beside it and renamed over it. A running binary can be
// renamed over on Unix; on Windows it can only be renamed away, which is done first and undone if the new one cannot take its place.
func replace(exe string, content []byte) error {
	dir := filepath.Dir(exe)
	tmp, err := os.CreateTemp(dir, ".sleipnir-update-*")
	if err != nil {
		return fmt.Errorf("cannot write in %s (%v): run `sleipnir update` with the rights to change it, or install by hand from the release page", dir, err)
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, 0o755); err != nil {
		return err
	}
	if err := os.Rename(name, exe); err == nil {
		return nil
	}
	old := exe + ".old"
	_ = os.Remove(old)
	if err := os.Rename(exe, old); err != nil {
		return fmt.Errorf("cannot replace %s: %w", exe, err)
	}
	if err := os.Rename(name, exe); err != nil {
		_ = os.Rename(old, exe)
		return fmt.Errorf("cannot replace %s: %w", exe, err)
	}
	return nil
}

// CleanUp removes what an update on Windows left of the binary it replaced.
func CleanUp(exe string) { _ = os.Remove(exe + ".old") }
