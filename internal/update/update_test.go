package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func tarGz(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, f := range []struct {
		n string
		b []byte
	}{{"README.md", []byte("readme")}, {name, content}} {
		if err := tw.WriteHeader(&tar.Header{Name: f.n, Mode: 0o755, Size: int64(len(f.b)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		tw.Write(f.b)
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func zipOf(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create(name)
	w.Write(content)
	zw.Close()
	return buf.Bytes()
}

// release is a server that is GitHub, with one release v0.2.0 that is ahead of the build by 43 commits.
func release(t *testing.T, goos string, archive []byte, sumOf []byte) (Options, *httptest.Server) {
	t.Helper()
	var ts *httptest.Server
	o := Options{GOOS: goos, GOARCH: "amd64", Now: func() time.Time { return time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC) }}
	name := ArchiveName(o, "v0.2.0")
	sum := sha256.Sum256(sumOf)
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/"+Repo+"/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"tag_name":"v0.2.0","html_url":"https://example.test/r","assets":[{"name":%q,"browser_download_url":"%s/dl/%s"},{"name":"checksums.txt","browser_download_url":"%s/dl/checksums.txt"}]}`, name, ts.URL, name, ts.URL)
	})
	mux.HandleFunc("/repos/"+Repo+"/compare/", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "abc1234...v0.2.0") {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `{"ahead_by":43}`)
	})
	mux.HandleFunc("/dl/"+name, func(w http.ResponseWriter, r *http.Request) { w.Write(archive) })
	mux.HandleFunc("/dl/checksums.txt", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s  %s\n%s  other_file\n", hex.EncodeToString(sum[:]), name, strings.Repeat("0", 64))
	})
	ts = httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	o.API = ts.URL
	return o, ts
}

func TestCheckSaysHowFarAheadTheLatestReleaseIsAndTheNoticeSaysIt(t *testing.T) {
	o, _ := release(t, "linux", nil, nil)
	o.CachePath = filepath.Join(t.TempDir(), "update.json")
	st, err := Check(context.Background(), o, "0.1.0", "abc1234")
	if err != nil || st.Latest != "v0.2.0" || st.Behind != 43 {
		t.Fatalf("Check = %+v, %v", st, err)
	}
	want := "A newer Sleipnir is out: v0.2.0, 43 commits ahead of yours. Run `sleipnir update`."
	if got := Notice("0.1.0", st); got != want {
		t.Errorf("Notice = %q", got)
	}
	if got := Notice("0.2.0", st); got != "" {
		t.Errorf("the latest version is told nothing: %q", got)
	}
	if got := Notice("dev", st); got != "" {
		t.Errorf("a build from source is told nothing: %q", got)
	}
	// the commit is unknown to GitHub (a local build): the notice still says there is a newer one
	st, _ = Check(context.Background(), o, "0.1.0", "ffffff")
	if st.Behind != -1 || !strings.Contains(Notice("0.1.0", st), "v0.2.0. Run") {
		t.Errorf("without the count: %+v %q", st, Notice("0.1.0", st))
	}
}

func TestRefreshKeepsTheAnswerForADayAndAFailureIsNotTriedAgainAtOnce(t *testing.T) {
	o, ts := release(t, "linux", nil, nil)
	o.CachePath = filepath.Join(t.TempDir(), "update.json")
	Refresh(context.Background(), o, "0.1.0", "abc1234")
	st, ok := Cached(o)
	if !ok || st.Latest != "v0.2.0" {
		t.Fatalf("cached: %+v %v", st, ok)
	}
	ts.Close() // no network from now on
	Refresh(context.Background(), o, "0.1.0", "abc1234")
	if st2, _ := Cached(o); st2.Latest != "v0.2.0" {
		t.Errorf("a fresh answer is kept and not asked again: %+v", st2)
	}
	later := o
	later.Now = func() time.Time { return o.Now().Add(48 * time.Hour) }
	Refresh(context.Background(), later, "0.1.0", "abc1234") // stale, and the network is gone
	st3, _ := Cached(later)
	if st3.Latest != "v0.2.0" || !st3.Checked.Equal(later.Now()) {
		t.Errorf("a failed check keeps what was known and counts as a check: %+v", st3)
	}
}

func TestNewerComparesVersions(t *testing.T) {
	for _, c := range []struct {
		v, tag string
		want   bool
	}{
		{"0.1.0", "v0.1.1", true}, {"0.1.0", "v0.1.0", false}, {"0.2.0", "v0.1.9", false}, {"0.9.0", "v0.10.0", true},
		{"1.0.0-rc1", "v1.0.1", true}, {"dev", "v9.9.9", false}, {"0.1.0", "nightly", false},
	} {
		if got := Newer(c.v, c.tag); got != c.want {
			t.Errorf("Newer(%q, %q) = %v", c.v, c.tag, got)
		}
	}
}

func TestInstallReplacesTheBinaryAfterTheChecksumHolds(t *testing.T) {
	newBin := []byte("#!/bin/sh\necho new\n")
	for _, goos := range []string{"linux", "windows"} {
		name := "sleipnir"
		archive := tarGz(t, name, newBin)
		if goos == "windows" {
			name = "sleipnir.exe"
			archive = zipOf(t, "sleipnir_0.2.0_windows_amd64/"+name, newBin)
		}
		o, _ := release(t, goos, archive, archive)
		rel, err := Latest(context.Background(), o)
		if err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		exe := filepath.Join(dir, name)
		os.WriteFile(exe, []byte("old"), 0o755)
		if err := Install(context.Background(), o, rel, exe); err != nil {
			t.Fatalf("%s: %v", goos, err)
		}
		if got, _ := os.ReadFile(exe); !bytes.Equal(got, newBin) {
			t.Errorf("%s: the binary is %q", goos, got)
		}
		if ents, _ := os.ReadDir(dir); len(ents) != 1 {
			t.Errorf("%s: nothing is left beside the binary: %v", goos, ents)
		}
	}
}

func TestInstallRefusesAnArchiveThatIsNotTheOneTheReleaseSums(t *testing.T) {
	archive := tarGz(t, "sleipnir", []byte("new"))
	o, _ := release(t, "linux", archive, []byte("something else entirely"))
	rel, _ := Latest(context.Background(), o)
	exe := filepath.Join(t.TempDir(), "sleipnir")
	os.WriteFile(exe, []byte("old"), 0o755)
	err := Install(context.Background(), o, rel, exe)
	if err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("err = %v", err)
	}
	if got, _ := os.ReadFile(exe); string(got) != "old" {
		t.Errorf("a bad download changed the binary: %q", got)
	}
}

func TestInstallSaysWhenTheReleaseHasNoArchiveForThisMachine(t *testing.T) {
	o, _ := release(t, "linux", tarGz(t, "sleipnir", []byte("x")), nil)
	rel, _ := Latest(context.Background(), o)
	o.GOARCH = "riscv64"
	err := Install(context.Background(), o, rel, filepath.Join(t.TempDir(), "sleipnir"))
	if err == nil || !strings.Contains(err.Error(), "linux/riscv64") {
		t.Errorf("err = %v", err)
	}
}
