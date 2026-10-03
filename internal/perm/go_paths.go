package perm

import (
	"path/filepath"
	"strings"
)

// goPathUses distinguishes package inputs from literal flag values. It does not
// authorize execution: go test and flags that execute programs retain their
// command-level approval policy. Unknown flags and test-binary arguments keep
// the conservative unknown-command analysis.
func goPathUses(args []string) ([]pathUse, bool) {
	if len(args) == 0 || !inList([]string{"test", "list", "build", "vet"}, args[0]) {
		return nil, false
	}
	sub, rest := args[0], args[1:]
	var uses []pathUse
	packages, closed := false, false
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		if !strings.HasPrefix(a, "-") || a == "-" {
			if closed {
				uses = append(uses, unknownUses(rest[i:])...)
				break
			}
			packages = true
			uses = append(uses, goPackageUse(a))
			continue
		}
		if sub == "test" && packages {
			closed = true // Go accepts one contiguous package list.
		}
		if a == "--" && sub != "test" {
			for _, p := range rest[i+1:] {
				uses = append(uses, goPackageUse(p))
				packages = true
			}
			break
		}
		name, value, inline := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if sub == "test" && strings.HasPrefix(name, "test.") {
			short := strings.TrimPrefix(name, "test.")
			if !goTestFlags[short] {
				uses = append(uses, unknownUses(rest[i:])...)
				break
			}
			name = short
		}
		kind, known := goPathFlag(sub, name)
		if !known || (sub != "test" && packages) {
			uses = append(uses, unknownUses(rest[i:])...)
			break
		}
		if kind == goBoolFlag {
			continue
		}
		if !inline {
			if i+1 >= len(rest) {
				break // Go reports the missing value; it is not a package.
			}
			i++
			value = rest[i]
		}
		switch kind {
		case goReadFlag:
			uses = append(uses, pathUse{raw: value})
		case goWriteFlag:
			uses = append(uses, pathUse{raw: value, write: true, tree: true})
		case goUnknownFlag:
			uses = append(uses, unknownUses([]string{value})...)
		}
	}
	if !packages {
		uses = append(uses, pathUse{raw: ".", tree: true})
	}
	return uses, true
}

// goPackageUse maps local package selectors to input trees without cleaning dot
// segments before symlink resolution. An ellipsis can span directory boundaries;
// checking its literal parent tree conservatively includes every possible match.
// Import paths are resolved by Go's module configuration, not by the shell cwd.
func goPackageUse(raw string) pathUse {
	p := filepath.ToSlash(raw)
	local := p == "." || p == ".." || strings.HasPrefix(p, "./") || strings.HasPrefix(p, "../") ||
		filepath.VolumeName(p) != "" || strings.HasPrefix(p, "/") ||
		strings.HasPrefix(p, "~") || strings.HasPrefix(p, "$")
	if strings.HasSuffix(p, ".go") && !strings.Contains(p, "...") {
		return pathUse{raw: p}
	}
	if !local {
		return pathUse{raw: raw, unresolved: true}
	}
	if i := strings.Index(p, "..."); i >= 0 {
		// Keep the slash: /... must remain the filesystem root, and link/../...
		// must reach the resolver with its original traversal semantics.
		p = p[:strings.LastIndexByte(p[:i], '/')+1]
	}
	return pathUse{raw: p, tree: true}
}

type goFlagKind uint8

const (
	goBoolFlag goFlagKind = iota
	goValueFlag
	goReadFlag
	goWriteFlag
	goUnknownFlag
)

// goPathFlag accepts only flags known to the selected subcommand. In particular,
// list-only flags must not hide file operands passed to a custom test flag.
func goPathFlag(sub, name string) (goFlagKind, bool) {
	if sub != "test" && goTestFlags[name] && name != "v" {
		return 0, false
	}
	if inList([]string{"f", "e", "compiled", "deps", "export", "find", "m", "test", "u", "versions", "retracted"}, name) && sub != "list" {
		return 0, false
	}
	if (name == "c" || name == "vet" || name == "exec") && sub != "test" {
		return 0, false
	}
	if name == "vettool" && sub != "vet" {
		return 0, false
	}
	kind, ok := goPathFlags[name]
	return kind, ok
}

// goTestFlags are the flags Go also accepts with the test. prefix. A prefixed
// build flag is an unknown test-binary argument, not a package-list delimiter.
var goTestFlags = map[string]bool{
	"bench": true, "benchmem": true, "benchtime": true, "blockprofile": true,
	"blockprofilerate": true, "count": true, "coverprofile": true, "cpu": true,
	"cpuprofile": true, "failfast": true, "fullpath": true, "fuzz": true,
	"fuzzminimizetime": true, "fuzztime": true, "list": true, "memprofile": true,
	"memprofilerate": true, "mutexprofile": true, "mutexprofilefraction": true,
	"outputdir": true, "parallel": true, "run": true, "short": true,
	"shuffle": true, "skip": true, "timeout": true, "trace": true, "v": true,
}

// goPathFlags records argument shape and file effects, not an execution allowlist.
// In particular, -args, --, and unknown test flags stop package interpretation.
var goPathFlags = map[string]goFlagKind{
	"a": goBoolFlag, "n": goBoolFlag, "x": goBoolFlag, "v": goBoolFlag,
	"asan": goBoolFlag, "msan": goBoolFlag, "race": goBoolFlag, "trimpath": goBoolFlag,
	"work": goBoolFlag, "cover": goBoolFlag, "linkshared": goBoolFlag, "modcacherw": goBoolFlag,
	"buildvcs": goBoolFlag, "json": goBoolFlag,
	"e": goBoolFlag, "compiled": goBoolFlag, "deps": goBoolFlag, "export": goBoolFlag,
	"find": goBoolFlag, "m": goBoolFlag, "test": goBoolFlag, "u": goBoolFlag,
	"versions": goBoolFlag, "retracted": goBoolFlag,
	"benchmem": goBoolFlag, "short": goBoolFlag, "fullpath": goBoolFlag,
	"c": goBoolFlag, "failfast": goBoolFlag,
	"f": goValueFlag, "tags": goValueFlag, "mod": goValueFlag, "p": goValueFlag,
	"covermode": goValueFlag, "coverpkg": goValueFlag, "vet": goValueFlag,
	"bench": goValueFlag, "benchtime": goValueFlag, "count": goValueFlag, "cpu": goValueFlag,
	"fuzz": goValueFlag, "fuzzminimizetime": goValueFlag, "fuzztime": goValueFlag,
	"list": goValueFlag, "parallel": goValueFlag, "run": goValueFlag, "shuffle": goValueFlag,
	"skip": goValueFlag, "timeout": goValueFlag, "blockprofilerate": goValueFlag,
	"memprofilerate": goValueFlag, "mutexprofilefraction": goValueFlag,
	"modfile": goReadFlag, "overlay": goReadFlag, "pgo": goReadFlag,
	"o": goWriteFlag, "outputdir": goWriteFlag, "coverprofile": goWriteFlag,
	"cpuprofile": goWriteFlag, "memprofile": goWriteFlag, "blockprofile": goWriteFlag,
	"mutexprofile": goWriteFlag, "trace": goWriteFlag,
	"gcflags": goUnknownFlag, "gccgoflags": goUnknownFlag, "ldflags": goUnknownFlag,
	"asmflags": goUnknownFlag, "buildmode": goUnknownFlag, "compiler": goUnknownFlag,
	"installsuffix": goUnknownFlag, "pkgdir": goUnknownFlag,
	"vettool": goUnknownFlag, "toolexec": goUnknownFlag, "exec": goUnknownFlag,
}
