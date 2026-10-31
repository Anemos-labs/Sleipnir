package sandbox

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Toolchain is what a sandbox chosen for a project has: the image with the project's language in it, and the command, if any,
// that fetches the project's dependencies before the network goes off.
type Toolchain struct {
	// Lang names the language ("go", "rust", ...); "" is the default image, for a project of no language it knows.
	Lang string
	// Image is the image to run.
	Image string
	// Manifest is the file the language was read from (go.mod, Cargo.toml, ...), relative to the project.
	Manifest string
	// Fetch is the command that downloads the dependencies the manifest names into the container's HOME, run from the project
	// with the network on, before any command of the session. It is set only for a fetch that runs none of the project's code
	// and nothing the project's configuration names: go mod download (a module's settings cannot name a program or a proxy).
	// cargo fetch has none: a project's .cargo/config.toml can name a credential provider, a program cargo runs. A package
	// manager that runs install scripts or builds packages to resolve them has none either.
	Fetch []string
}

// goVersionRE is the version a go.mod's go or toolchain line names: go1.22.3, 1.22.3, 1.22.
var goVersionRE = regexp.MustCompile(`^(?:go)?([1-9][0-9]?)\.([0-9]{1,3})(?:\.[0-9]{1,3})?(?:rc[0-9]+)?$`)

// Detect is the toolchain for the project in dir, from the manifests at its top: go.mod, Cargo.toml, a Maven or Gradle
// build, a Python project, a Gemfile, package.json, in that order (a repository with a go.mod and a package.json is a Go
// service with a web front, more often than not). A project of none of them gets DefaultImage.
func Detect(dir string) Toolchain {
	has := func(name string) bool {
		fi, err := os.Stat(filepath.Join(dir, name))
		return err == nil && fi.Mode().IsRegular()
	}
	switch {
	case has("go.mod"):
		return Toolchain{Lang: "go", Image: "golang:" + goImageTag(filepath.Join(dir, "go.mod")) + "-bookworm", Manifest: "go.mod",
			Fetch: []string{"go", "mod", "download"}}
	case has("Cargo.toml"):
		return Toolchain{Lang: "rust", Image: "rust:1-bookworm", Manifest: "Cargo.toml"}
	case has("pom.xml"):
		return Toolchain{Lang: "java", Image: "maven:3-eclipse-temurin-21", Manifest: "pom.xml"}
	case has("build.gradle"), has("build.gradle.kts"):
		m := "build.gradle"
		if !has(m) {
			m += ".kts"
		}
		return Toolchain{Lang: "java", Image: "gradle:jdk21", Manifest: m}
	case has("pyproject.toml"), has("requirements.txt"), has("setup.py"):
		m := "pyproject.toml"
		for _, n := range []string{"pyproject.toml", "requirements.txt", "setup.py"} {
			if has(n) {
				m = n
				break
			}
		}
		return Toolchain{Lang: "python", Image: "python:3-bookworm", Manifest: m}
	case has("Gemfile"):
		return Toolchain{Lang: "ruby", Image: "ruby:3-bookworm", Manifest: "Gemfile"}
	case has("package.json"):
		return Toolchain{Lang: "node", Image: "node:lts-bookworm", Manifest: "package.json"}
	}
	return Toolchain{Image: DefaultImage}
}

// goImageTag is the golang image tag for a go.mod: the major.minor of its toolchain line, else of its go line (the newest patch
// of that release satisfies either), else "1". What the file says is checked against the shape of a version before it goes
// into an image name.
func goImageTag(gomod string) string {
	f, err := os.Open(gomod)
	if err != nil {
		return "1"
	}
	defer f.Close()
	var goLine, toolchain string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line, _, _ := strings.Cut(sc.Text(), "//")
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		switch fields[0] {
		case "go":
			goLine = fields[1]
		case "toolchain":
			toolchain = fields[1]
		}
	}
	for _, v := range []string{toolchain, goLine} {
		if m := goVersionRE.FindStringSubmatch(v); m != nil {
			return m[1] + "." + m[2]
		}
	}
	return "1"
}
