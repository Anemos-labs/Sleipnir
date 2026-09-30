package repocheck

import (
	"regexp"
	"sort"
	"strings"
	"testing"
)

// yamlList reads the value of "key:" from text as a list, whether it is written inline ([a, b]) or as a block. It returns
// the list and whether the key was found. A key that starts a list item ("- key: ...") counts.
func yamlList(text, key string) ([]string, bool) {
	ls := lines(text)
	for i, l := range ls {
		if blankOrComment(l) {
			continue
		}
		trimmed := strings.TrimSpace(l)
		keyIndent := indentOf(l)
		if rest, ok := strings.CutPrefix(trimmed, "- "); ok {
			trimmed, keyIndent = rest, keyIndent+2
		}
		k, v, ok := strings.Cut(trimmed, ":")
		if !ok || k != key {
			continue
		}
		return listValue(strings.TrimSpace(uncomment(v)), ls[i+1:], keyIndent), true
	}
	return nil, false
}

var (
	flowPair  = regexp.MustCompile(`goos:\s*(\w+)\s*,\s*goarch:\s*(\w+)`)
	goosKey   = regexp.MustCompile(`^\s*(?:-\s+)?goos:\s*(\w+)\s*$`)
	goarchKey = regexp.MustCompile(`^\s*(?:-\s+)?goarch:\s*(\w+)\s*$`)
)

// ignoredTargets reads the ignore: entries of the builds, either as flow maps ({goos: windows, goarch: arm64}) or as
// blocks (a goos line and a goarch line).
func ignoredTargets(text string) map[string]bool {
	out := map[string]bool{}
	ls := lines(text)
	for i, l := range ls {
		if strings.TrimSpace(l) != "ignore:" {
			continue
		}
		var block []string
		for _, n := range ls[i+1:] {
			if !blankOrComment(n) && indentOf(n) <= indentOf(l) {
				break
			}
			block = append(block, n)
		}
		for _, m := range flowPair.FindAllStringSubmatch(strings.Join(block, "\n"), -1) {
			out[m[1]+"/"+m[2]] = true
		}
		goos := ""
		for _, n := range block {
			if m := goosKey.FindStringSubmatch(n); m != nil {
				goos = m[1]
			} else if m := goarchKey.FindStringSubmatch(n); m != nil && goos != "" {
				out[goos+"/"+m[1]] = true
				goos = ""
			}
		}
	}
	return out
}

// goreleaserTargets is every goos/goarch pair that .goreleaser.yaml builds: goos times goarch, minus the ignored pairs.
func goreleaserTargets(t *testing.T) map[string]bool {
	t.Helper()
	text := active(read(t, ".goreleaser.yaml"))
	goos, ok1 := yamlList(text, "goos")
	goarch, ok2 := yamlList(text, "goarch")
	if !ok1 || !ok2 || len(goos) == 0 || len(goarch) == 0 {
		t.Fatal(".goreleaser.yaml: no goos or goarch list found in the builds (this test reads `goos: [a, b]` and `goarch: [a, b]`, or the block form)")
	}
	ignored := ignoredTargets(text)
	out := map[string]bool{}
	for _, o := range goos {
		for _, a := range goarch {
			if !ignored[o+"/"+a] {
				out[o+"/"+a] = true
			}
		}
	}
	return out
}

var platform = regexp.MustCompile(`\b(linux|darwin|windows|freebsd|openbsd|netbsd|plan9|solaris)/(amd64|arm64|386|arm|riscv64|ppc64le|s390x)\b`)

func platformsIn(text string) map[string]bool {
	out := map[string]bool{}
	for _, m := range platform.FindAllString(text, -1) {
		out[m] = true
	}
	return out
}

func names(m map[string]bool) string {
	var s []string
	for k := range m {
		s = append(s, k)
	}
	sort.Strings(s)
	return strings.Join(s, " ")
}

// What goreleaser ships must be built and vetted by CI (and by a developer's scripts/check.sh) for every platform, or a
// file for one of them can rot until release day.
func TestCrossCompileCoversGoreleaserTargets(t *testing.T) {
	want := goreleaserTargets(t)
	if len(want) == 0 {
		t.Fatal(".goreleaser.yaml builds no target")
	}
	ci := workflowNamed(t, "ci.yml")
	cross := ci.job("cross")
	if cross == nil {
		t.Fatal("ci.yml has no cross job: platform-specific files would only be compiled on release day")
	}
	for _, src := range []struct{ name, text string }{
		{"the cross job of ci.yml", active(cross.Text)},
		{"scripts/check.sh", active(read(t, "scripts/check.sh"))},
	} {
		got := platformsIn(src.text)
		for target := range want {
			if !got[target] {
				t.Errorf("%s does not build %s, which .goreleaser.yaml ships (it builds: %s)", src.name, target, names(got))
			}
		}
	}
	if !strings.Contains(active(cross.Text), "go vet") {
		t.Error("the cross job of ci.yml does not run go vet for each platform: a _windows_test.go or a _darwin.go file would not be type-checked")
	}
}

func TestGoreleaserTargetsParse(t *testing.T) {
	got, ok := yamlList("builds:\n  - goos: [linux, darwin, windows]\n    goarch: [amd64, arm64]\n", "goos")
	if !ok || strings.Join(got, ",") != "linux,darwin,windows" {
		t.Errorf("inline list: got %v, %v", got, ok)
	}
	got, _ = yamlList("a:\n  goarch:\n    - amd64\n    - arm64 # two\n  next: x\n", "goarch")
	if strings.Join(got, ",") != "amd64,arm64" {
		t.Errorf("block list: got %v", got)
	}
	flow := ignoredTargets("a:\n  ignore:\n    - {goos: windows, goarch: arm64}\n    - {goos: linux, goarch: riscv64}\n  next: x\n")
	if names(flow) != "linux/riscv64 windows/arm64" {
		t.Errorf("flow ignore entries: got %q", names(flow))
	}
	block := ignoredTargets("a:\n  ignore:\n    - goos: windows\n      goarch: arm64\n  goarch: amd64\n")
	if names(block) != "windows/arm64" {
		t.Errorf("block ignore entries: got %q", names(block))
	}
}

// scripts/install.sh downloads the archive that goreleaser makes; if the two disagree on a name, the installer 404s for
// everyone on the first release.
func TestInstallScriptMatchesGoreleaser(t *testing.T) {
	gr := active(read(t, ".goreleaser.yaml"))
	install := read(t, "scripts/install.sh")

	m := regexp.MustCompile(`name_template:\s*"?(sleipnir[^"\n]*)"?`).FindStringSubmatch(gr)
	if m == nil {
		t.Fatal(".goreleaser.yaml: no archive name_template starting with sleipnir found")
	}
	tmpl := strings.TrimSpace(m[1])

	a := regexp.MustCompile(`(?m)^\s*archive="([^"]+)"`).FindStringSubmatch(install)
	if a == nil {
		t.Fatal(`scripts/install.sh: no archive="..." line found`)
	}
	name := strings.NewReplacer("${version}", "{{ .Version }}", "${os}", "{{ .Os }}", "${arch}", "{{ .Arch }}").Replace(a[1])
	if name != tmpl+".tar.gz" {
		t.Errorf("scripts/install.sh downloads %q (as a goreleaser template), but .goreleaser.yaml's archives are named %q + the extension .tar.gz: they must agree", name, tmpl)
	}
	formats, ok := yamlList(gr, "formats")
	if !ok || len(formats) == 0 || formats[0] != "tar.gz" {
		t.Errorf(".goreleaser.yaml: the default archive format is %v, but scripts/install.sh unpacks a .tar.gz", formats)
	}
	if ov := regexp.MustCompile(`(?s)format_overrides:(.*?)(?:\n\S|\z)`).FindStringSubmatch(gr); ov != nil {
		for _, o := range regexp.MustCompile(`goos:\s*(\w+)`).FindAllStringSubmatch(ov[1], -1) {
			if o[1] == "linux" || o[1] == "darwin" {
				t.Errorf(".goreleaser.yaml overrides the archive format for %s, but scripts/install.sh installs there and expects a .tar.gz", o[1])
			}
		}
	}
	if c := regexp.MustCompile(`checksum:\s*\n\s+name_template:\s*(\S+)`).FindStringSubmatch(gr); c == nil || !strings.Contains(install, `"$base/`+strings.Trim(c[1], `"`)+`"`) {
		t.Error("scripts/install.sh does not download the checksum file that .goreleaser.yaml names (checksum: name_template)")
	}
	if b := regexp.MustCompile(`(?m)^\s+binary:\s*(\S+)`).FindStringSubmatch(gr); b == nil || !strings.Contains(install, "-C \"$tmp\" "+b[1]) {
		t.Error("scripts/install.sh unpacks a binary by another name than the `binary:` of .goreleaser.yaml")
	}
	mod := regexp.MustCompile(`(?m)^module\s+github\.com/(\S+)`).FindStringSubmatch(read(t, "go.mod"))
	repo := regexp.MustCompile(`(?m)^repo="([^"]+)"`).FindStringSubmatch(install)
	if mod == nil || repo == nil || !strings.EqualFold(mod[1], repo[1]) {
		t.Errorf("scripts/install.sh downloads from %v, but go.mod's module is github.com/%v", repo, mod)
	}
}
