package sandbox

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func writeFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// The image follows the project's language, and only a fetch that runs none of the project's code is offered.
func TestDetectPicksTheLanguagesImage(t *testing.T) {
	for _, c := range []struct {
		files map[string]string
		lang  string
		image string
		fetch bool
	}{
		{map[string]string{"go.mod": "module x\n\ngo 1.24.2\n"}, "go", "golang:1.24-bookworm", true},
		{map[string]string{"go.mod": "module x\n\ngo 1.22\n\ntoolchain go1.23.4\n"}, "go", "golang:1.23-bookworm", true},
		{map[string]string{"go.mod": "module x\n"}, "go", "golang:1-bookworm", true},
		{map[string]string{"go.mod": "module x\n\ngo 1.21 // the oldest we support\n"}, "go", "golang:1.21-bookworm", true},
		// what a go.mod says goes into an image name only when it has the shape of a version
		{map[string]string{"go.mod": "module x\n\ngo 1.22;curl\n"}, "go", "golang:1-bookworm", true},
		{map[string]string{"go.mod": "module x\ngo 1.24\n", "package.json": "{}"}, "go", "golang:1.24-bookworm", true},
		// cargo fetch would run a credential provider a project's .cargo/config.toml names, with the network on
		{map[string]string{"Cargo.toml": "[package]\n"}, "rust", "rust:1-bookworm", false},
		{map[string]string{"pom.xml": "<project/>"}, "java", "maven:3-eclipse-temurin-21", false},
		{map[string]string{"build.gradle.kts": ""}, "java", "gradle:jdk21", false},
		{map[string]string{"requirements.txt": "flask\n"}, "python", "python:3-bookworm", false},
		{map[string]string{"Gemfile": ""}, "ruby", "ruby:3-bookworm", false},
		{map[string]string{"package.json": "{}"}, "node", "node:lts-bookworm", false},
		{map[string]string{"README.md": "hi"}, "", DefaultImage, false},
	} {
		tc := Detect(writeFiles(t, c.files))
		if tc.Lang != c.lang || tc.Image != c.image || (len(tc.Fetch) > 0) != c.fetch {
			t.Errorf("Detect(%v) = %+v, want %s %s fetch=%v", c.files, tc, c.lang, c.image, c.fetch)
		}
		if err := checkImage(tc.Image); err != nil {
			t.Errorf("Detect(%v) chose an image checkImage refuses: %v", c.files, err)
		}
	}
	if tc := Detect(writeFiles(t, map[string]string{"go.mod": "module x\n"})); !slices.Equal(tc.Fetch, []string{"go", "mod", "download"}) {
		t.Fatalf("go fetch = %v", tc.Fetch)
	}
}

// A sandbox whose image is chosen for a Go module fetches the modules with the network on, then has none.
func TestStartFetchesThenGoesOffline(t *testing.T) {
	if os.Getenv("SLEIPNIR_TEST_SANDBOX_FETCH") == "" {
		t.Skip("pulls golang and fetches a module from the network: set SLEIPNIR_TEST_SANDBOX_FETCH=1")
	}
	dir := writeFiles(t, map[string]string{
		"go.mod":  "module example.com/x\n\ngo 1.22\n\nrequire golang.org/x/text v0.14.0\n",
		"go.sum":  "golang.org/x/text v0.14.0 h1:ScX5w1eTa3QqT8oi6+ziP7dTV1S2+ALU0bI+0zXKWiQ=\ngolang.org/x/text v0.14.0/go.mod h1:18ZOQIKpY8NJVqYksKHtTdi31H5itFRjB5/qKTNYzSU=\n",
		"main.go": "package main\n\nimport (\n\t\"fmt\"\n\n\t\"golang.org/x/text/language\"\n)\n\nfunc main() { fmt.Println(language.English) }\n",
	})
	b := startBox(t, dir, false, "")
	if b.Toolchain.Lang != "go" || b.Spec.Image != "golang:1.22-bookworm" {
		t.Fatalf("toolchain %+v image %s", b.Toolchain, b.Spec.Image)
	}
	if b.Fetched != "go mod download fetched the dependencies" {
		t.Fatalf("fetch: %s", b.Fetched)
	}
	out, err := runIn(b, dir, "go", "run", ".")
	if err != nil || string(out) != "en\n" {
		t.Fatalf("go run offline: %v\n%s", err, out)
	}
	if out, err := runIn(b, dir, "sh", "-c", "curl -sS --max-time 5 https://proxy.golang.org >/dev/null"); err == nil {
		t.Fatalf("the network is still on after the fetch: %s", out)
	}
}
