package taskgen

import (
	"path"
	"strings"
)

// Kind says what a changed file is for the purpose of mining a task.
type Kind int

const (
	// Other is anything that is neither source nor tests: documentation,
	// configuration, lock files. It travels with the reference solution.
	Other Kind = iota
	// Source is production code.
	Source
	// Test is a test file: code that the hidden verifier suite is made of.
	Test
	// TestSupport is data or helper code that lives with the tests (golden
	// files, fixtures): hidden and protected together with them.
	TestSupport
)

func (k Kind) String() string {
	switch k {
	case Source:
		return "source"
	case Test:
		return "test"
	case TestSupport:
		return "test-support"
	}
	return "other"
}

// Language names understood by the generators.
const (
	Go     = "go"
	Python = "python"
	JS     = "js"
	TS     = "ts"
	Rust   = "rust"
	Java   = "java"
)

// Classify returns the language and Kind of a repository-relative path. Files
// that belong to no supported language are (“”, Other) unless they sit in a test
// fixture directory, in which case they are TestSupport of language "".
func Classify(p string) (lang string, kind Kind) {
	p = path.Clean(p)
	base := path.Base(p)
	ext := strings.ToLower(path.Ext(base))
	parts := strings.Split(p, "/")
	dirs := parts[:len(parts)-1]

	hasDir := func(names ...string) bool {
		for _, d := range dirs {
			for _, n := range names {
				if d == n {
					return true
				}
			}
		}
		return false
	}
	if hasDir("vendor", "node_modules", "third_party", ".git", "dist", "build", "target", "__pycache__") {
		return "", Other
	}
	fixtureDir := hasDir("testdata", "fixtures", "__fixtures__", "__snapshots__", "__mocks__", "snapshots", "golden")

	switch ext {
	case ".go":
		switch {
		case strings.HasSuffix(base, "_test.go"):
			return Go, Test
		case fixtureDir:
			return Go, TestSupport
		}
		return Go, Source
	case ".py":
		switch {
		case strings.HasPrefix(base, "test_"), strings.HasSuffix(base, "_test.py"), base == "conftest.py":
			return Python, Test
		case hasDir("tests", "test", "testing"):
			return Python, TestSupport // helpers next to the tests
		case fixtureDir:
			return Python, TestSupport
		}
		return Python, Source
	case ".js", ".jsx", ".mjs", ".cjs", ".ts", ".tsx", ".mts", ".cts":
		l := JS
		if strings.HasPrefix(ext, ".ts") || ext == ".mts" || ext == ".cts" {
			l = TS
		}
		low := strings.ToLower(base)
		switch {
		case strings.Contains(low, ".test."), strings.Contains(low, ".spec."):
			return l, Test
		case hasDir("__tests__", "tests", "test", "e2e"):
			return l, TestSupport
		case fixtureDir:
			return l, TestSupport
		case strings.Contains(low, ".config."), strings.HasSuffix(low, ".d.ts"), low == "gulpfile.js", low == "gruntfile.js":
			return "", Other
		}
		return l, Source
	case ".rs":
		switch {
		case hasDir("tests"):
			if dirs[len(dirs)-1] != "tests" {
				return Rust, TestSupport // tests/common/mod.rs and friends
			}
			return Rust, Test
		case base == "tests.rs", strings.HasSuffix(base, "_test.rs"), strings.HasSuffix(base, "_tests.rs"):
			return Rust, Test
		case fixtureDir:
			return Rust, TestSupport
		case base == "build.rs":
			return "", Other
		}
		return Rust, Source
	case ".java":
		switch {
		case strings.HasSuffix(base, "Test.java"), strings.HasSuffix(base, "Tests.java"), strings.HasSuffix(base, "IT.java"),
			strings.HasPrefix(base, "Test") && ext == ".java" && hasDir("test"):
			return Java, Test
		case containsSeq(dirs, "src", "test"):
			return Java, TestSupport
		}
		return Java, Source
	}
	if fixtureDir || containsSeq(dirs, "src", "test") || hasDir("__tests__") {
		return "", TestSupport
	}
	return "", Other
}

func containsSeq(dirs []string, a, b string) bool {
	for i := 0; i+1 < len(dirs); i++ {
		if dirs[i] == a && dirs[i+1] == b {
			return true
		}
	}
	return false
}
