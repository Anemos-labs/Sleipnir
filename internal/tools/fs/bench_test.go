package fs

import (
	"fmt"
	"strings"
	"testing"
)

// grep and glob are what an agent explores a repository with: the commonest tool calls of a session, run on trees of thousands of files.
// The pure-Go engine is the one every machine has; ripgrep is used where it is installed.
func benchTree(b *testing.B, files int) map[string]string {
	b.Helper()
	m := make(map[string]string, files)
	for i := 0; i < files; i++ {
		var sb strings.Builder
		fmt.Fprintf(&sb, "package pkg%d\n\n", i%40)
		for j := 0; j < 60; j++ {
			fmt.Fprintf(&sb, "func Func%d_%d(x int) int { return x + %d } // helper %d\n", i, j, j, j)
		}
		sb.WriteString("// TODO(rounding): handle the case where the total is a half cent\n")
		m[fmt.Sprintf("pkg%d/file%d.go", i%40, i)] = sb.String()
	}
	return m
}

func BenchmarkGrep(b *testing.B) {
	env := testEnv(b)
	tree(b, env.Cwd, benchTree(b, 2000))
	for _, e := range engines(b) {
		for name, in := range map[string]map[string]any{
			"literal, few hits":  {"pattern": "TODO(rounding)"},
			"regexp, many hits":  {"pattern": `Func1[0-9]_5\(`},
			"files with matches": {"pattern": "helper 7", "output_mode": "files_with_matches"},
		} {
			b.Run(e.name+"/"+name, func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					if out := grep(b, e.tool, env, in); out == "" {
						b.Fatal("no output")
					}
				}
			})
		}
	}
}
