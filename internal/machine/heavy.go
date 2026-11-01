package machine

import (
	"regexp"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/shellparse"
)

// Heavy reports whether a shell command line runs a build, a test suite, an install or a bundler: a program that sizes itself
// for every core and much of the memory of the machine. It is a lexical guess over the commands the line would start
// (shellparse, wrappers such as env, nice and timeout peeled), made to err towards light: a light command never waits for a
// slot, so a missed build costs no more than it did before there were slots.
func Heavy(command string) bool {
	for _, c := range shellparse.Parse(command).Commands {
		if heavyProgram(baseName(c.Program), c.Args) {
			return true
		}
	}
	return false
}

// heavyAlways are programs that are a build, a test run or a type check whatever their arguments.
var heavyAlways = set(
	"make", "gmake", "ninja", "cmake", "meson", "scons", "bazel", "bazelisk", "buck", "buck2", "pants",
	"gradle", "gradlew", "mvn", "mvnw", "sbt", "ant", "lein",
	"tsc", "vue-tsc", "webpack", "rollup", "esbuild", "parcel", "turbo", "nx", "lerna",
	"jest", "vitest", "playwright", "cypress", "mocha", "ava", "karma",
	"pytest", "py.test", "tox", "nox", "mypy", "pyright",
	"gcc", "g++", "cc", "c++", "clang", "clang++", "rustc", "javac", "kotlinc", "scalac", "swiftc", "ghc",
	"xcodebuild", "msbuild", "golangci-lint", "eslint",
	"rebar3", "cabal", "stack", "dune", "nextest",
)

// heavySub are programs that are heavy when their first argument (the subcommand) is one of these.
var heavySub = map[string]map[string]bool{
	"go":         set("build", "test", "install", "vet", "run", "generate"),
	"cargo":      set("build", "b", "test", "t", "check", "c", "clippy", "run", "r", "bench", "install", "doc", "nextest", "rustc"),
	"npm":        set("install", "i", "ci", "add", "test", "t", "tst", "rebuild", "pack", "publish"),
	"pnpm":       set("install", "i", "add", "test", "t", "rebuild", "pack", "publish"),
	"yarn":       set("install", "add", "test", "pack", "publish"),
	"bun":        set("install", "i", "add", "test", "build"),
	"deno":       set("test", "compile", "bundle", "check"),
	"dotnet":     set("build", "test", "publish", "run", "pack", "restore"),
	"swift":      set("build", "test", "run"),
	"zig":        set("build", "test", "build-exe", "build-lib"),
	"mix":        set("compile", "test", "deps.get", "deps.compile", "release"),
	"flutter":    set("build", "test", "pub"),
	"dart":       set("test", "compile"),
	"docker":     set("build", "buildx", "compose"),
	"podman":     set("build", "compose"),
	"pip":        set("install", "wheel", "download"),
	"pip3":       set("install", "wheel", "download"),
	"uv":         set("sync", "pip", "build"),
	"poetry":     set("install", "build"),
	"pdm":        set("install", "build", "sync"),
	"hatch":      set("build", "test"),
	"bundle":     set("install"),
	"rake":       set("test", "spec", "build", "compile", "default"),
	"composer":   set("install", "update", "require"),
	"vite":       set("build"),
	"next":       set("build"),
	"nuxt":       set("build", "generate"),
	"astro":      set("build", "check"),
	"svelte-kit": set("build", "sync"),
	"ng":         set("build", "test", "e2e"),
}

// runners run a package's script: "npm run build", and for yarn and pnpm "yarn build" too.
var runners = map[string]bool{"npm": true, "pnpm": true, "yarn": true, "bun": true}

// launchers run the program named after their subcommand, which decides: "npx vitest", "pnpm exec tsc", "uv run pytest".
var launchers = map[string]map[string]bool{
	"npm":    set("exec", "x"),
	"pnpm":   set("exec", "dlx"),
	"yarn":   set("exec", "dlx"),
	"bun":    set("x", "exec"),
	"uv":     set("run", "tool"),
	"poetry": set("run"),
	"pdm":    set("run"),
	"hatch":  set("run"),
	"bundle": set("exec"),
}

// valueOptions are options of the package tools that take the next word as their value, so it is not their subcommand:
// "npm --prefix web run build".
var valueOptions = set("--prefix", "-C", "--dir", "--filter", "-F", "--workspace", "-w", "--cwd")

// heavyScript matches the name of a package script or a script file that builds, tests or checks.
var heavyScript = regexp.MustCompile(`(?i)(^|[-_:./])(build|test|tests|e2e|check|typecheck|lint|compile|ci|verify|bundle|dist)([-_:./]|$)`)

// pythonModules are the modules "python -m" runs that are builds or test runs.
var pythonModules = set("pytest", "unittest", "build", "pip", "mypy", "tox", "nox", "compileall", "nuitka", "PyInstaller")

// heavyProgram decides one simple command: name is the program's base name, args what follows it.
func heavyProgram(name string, args []string) bool {
	name = strings.TrimSuffix(strings.ToLower(name), ".exe")
	if name == "" {
		return false
	}
	if heavyAlways[name] {
		return true
	}
	sub, rest := firstWord(args)
	switch {
	case name == "sh" || name == "bash" || name == "zsh" || name == "dash" || name == "pwsh":
		return sub != "" && heavyProgram(baseName(sub), rest) // "bash scripts/test.sh"
	case name == "npx" || name == "pnpx" || name == "bunx":
		return sub != "" && heavyProgram(baseName(sub), rest)
	case launchers[name][sub]:
		s, r := firstWord(rest)
		return s != "" && heavyProgram(baseName(s), r)
	case runners[name] && (sub == "run" || sub == "run-script"):
		s, _ := firstWord(rest)
		return heavyScript.MatchString(s)
	case (name == "yarn" || name == "pnpm") && sub != "" && !heavySub[name][sub] && heavyScript.MatchString(sub):
		return true // "yarn build" runs the build script
	case name == "python" || name == "python3" || strings.HasPrefix(name, "python3."):
		for i, a := range args {
			if a == "-m" && i+1 < len(args) {
				return pythonModules[args[i+1]]
			}
		}
		return false
	case strings.HasSuffix(name, ".sh") || strings.HasSuffix(name, ".bash") || strings.HasSuffix(name, ".ps1"):
		return heavyScript.MatchString(strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(name, ".sh"), ".bash"), ".ps1"))
	}
	if subs, ok := heavySub[name]; ok {
		return subs[sub]
	}
	return false
}

// firstWord is the first argument that is not an option, and the arguments after it.
func firstWord(args []string) (string, []string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
		case valueOptions[a]:
			i++ // its value
		case !strings.HasPrefix(a, "-"):
			return a, args[i+1:]
		}
	}
	return "", nil
}

// baseName is the last element of a slash-separated program path, without a version suffix ("vitest@1.2" gives "vitest").
func baseName(p string) string {
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		p = p[i+1:]
	}
	if i := strings.LastIndexByte(p, '@'); i > 0 {
		p = p[:i]
	}
	return p
}

// set builds a membership map.
func set(words ...string) map[string]bool {
	m := make(map[string]bool, len(words))
	for _, w := range words {
		m[w] = true
	}
	return m
}
