package perm

import "strings"

// rustVersionQuery reports whether args only ask a Rust tool for its version: --version or -V, with --verbose or -v for the
// long form (cargo -vV, rustc --version --verbose).
func rustVersionQuery(args []string) bool {
	version := false
	for _, a := range args {
		switch a {
		case "--version", "-V", "-vV", "-Vv":
			version = true
		case "--verbose", "-v":
		default:
			return false
		}
	}
	return version
}

// analyseCargo allows the version query and cargo metadata --no-deps, which prints the workspace's own packages from its
// manifests. Without --no-deps cargo resolves the dependency graph: it may download crates, write Cargo.lock and run rustc (or a
// rustc wrapper a configuration names) to learn the target, so that form stays off the list. The options kept are the ones that
// only shape the output or forbid the network; --manifest-path is a file it reads. Any other option (--config, -Z, a toolchain
// selector such as +nightly) takes the command off the list.
func analyseCargo(args []string) safeAnalysis {
	if rustVersionQuery(args) {
		return safeAnalysis{ok: true}
	}
	if len(args) == 0 || args[0] != "metadata" {
		return safeAnalysis{why: unlisted}
	}
	var out safeAnalysis
	noDeps := false
	for i := 1; i < len(args); i++ {
		a := args[i]
		name, val, hasVal := strings.Cut(a, "=")
		switch {
		case a == "--no-deps":
			noDeps = true
		case inList([]string{"-q", "--quiet", "--offline", "--frozen", "--locked"}, a):
		case name == "--format-version" || name == "--color" || name == "--manifest-path":
			if !hasVal {
				if i+1 >= len(args) {
					return safeAnalysis{why: "cargo metadata " + name + " without a value"}
				}
				i++
				val = args[i]
			}
			if name == "--manifest-path" {
				out.uses = append(out.uses, pathUse{raw: val})
			}
		default:
			return safeAnalysis{why: "cargo metadata with " + quote(a) + ": only --no-deps, --format-version, --manifest-path, --color, --quiet, --offline, --frozen and --locked are on the read-only list"}
		}
	}
	if !noDeps {
		return safeAnalysis{why: "cargo metadata without --no-deps resolves dependencies, which may download crates, write Cargo.lock and run rustc"}
	}
	out.ok = true
	return out
}

// rustTargetTriple reports whether s is a target triple by name (x86_64-unknown-linux-gnu), not a path to a target
// specification file.
func rustTargetTriple(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

// analyseRustc allows the version query and rustc --print KIND, which prints facts about the compiler (sysroot, cfg,
// target-list, ...) without compiling anything, optionally for a --target given by triple. A --print KIND=PATH writes the answer
// to a file, and an input file or any other option makes it a compilation, so those stay off the list.
func analyseRustc(args []string) safeAnalysis {
	if rustVersionQuery(args) {
		return safeAnalysis{ok: true}
	}
	prints := 0
	for i := 0; i < len(args); i++ {
		name, val, hasVal := strings.Cut(args[i], "=")
		if name != "--print" && name != "--target" {
			return safeAnalysis{why: "rustc with " + quote(args[i]) + ": only --version and --print are on the read-only list"}
		}
		if !hasVal {
			if i+1 >= len(args) {
				return safeAnalysis{why: "rustc " + name + " without a value"}
			}
			i++
			val = args[i]
		}
		if name == "--target" {
			if !rustTargetTriple(val) {
				return safeAnalysis{why: "rustc --target with a target specification file"}
			}
			continue
		}
		if strings.Contains(val, "=") {
			return safeAnalysis{why: "rustc --print KIND=PATH writes to a file"}
		}
		prints++
	}
	if prints == 0 {
		return safeAnalysis{why: unlisted}
	}
	return safeAnalysis{ok: true}
}

// analyseRustup allows the queries that list what is installed: the version, rustup show (and its active-toolchain, home and
// profile forms), rustup toolchain list, and rustup target list and rustup component list with --installed. Every other
// subcommand, a list without --installed (which fetches the release channel's manifest), a +toolchain selector and any other
// option take the command off the list.
func analyseRustup(args []string) safeAnalysis {
	if rustVersionQuery(args) {
		return safeAnalysis{ok: true}
	}
	var pos []string
	installed, toolchain := false, false
	for i := 0; i < len(args); i++ {
		a := args[i]
		name, _, hasVal := strings.Cut(a, "=")
		switch {
		case inList([]string{"-v", "--verbose", "-q", "--quiet"}, a):
		case a == "--installed":
			installed = true
		case name == "--toolchain":
			if !hasVal {
				if i+1 >= len(args) {
					return safeAnalysis{why: "rustup --toolchain without a value"}
				}
				i++
			}
			toolchain = true
		case strings.HasPrefix(a, "-") || strings.HasPrefix(a, "+"):
			return safeAnalysis{why: "rustup with " + quote(a) + ": not on the read-only list"}
		default:
			pos = append(pos, a)
		}
	}
	key := strings.Join(pos, " ")
	switch key {
	case "show", "show active-toolchain", "show home", "show profile", "toolchain list":
		if !installed && !toolchain {
			return safeAnalysis{ok: true}
		}
	case "target list", "component list":
		if installed {
			return safeAnalysis{ok: true}
		}
		return safeAnalysis{why: "rustup " + key + " without --installed fetches the release channel's manifest"}
	}
	return safeAnalysis{why: unlisted}
}
