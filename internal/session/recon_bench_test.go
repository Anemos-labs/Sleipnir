package session

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The survey that seeds the shared pin is made once per session, before the first request, on whatever repository the person opens: a
// monorepo of tens of thousands of files must not make the first answer wait for it.
func BenchmarkBuildRecon(b *testing.B) {
	for _, files := range []int{500, 5000} {
		root := b.TempDir()
		for i := 0; i < files; i++ {
			dir := filepath.Join(root, fmt.Sprintf("pkg%d", i%60), fmt.Sprintf("sub%d", i%7))
			if err := os.MkdirAll(dir, 0o755); err != nil {
				b.Fatal(err)
			}
			var sb strings.Builder
			fmt.Fprintf(&sb, "package pkg%d\n\n", i%60)
			for j := 0; j < 30; j++ {
				fmt.Fprintf(&sb, "func Func%d_%d(x int) int { return x + %d }\n", i, j, j)
			}
			if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("file%d.go", i)), []byte(sb.String()), 0o644); err != nil {
				b.Fatal(err)
			}
		}
		for name, content := range map[string]string{"go.mod": "module example.com/big\n\ngo 1.24\n", "Makefile": "test:\n\tgo test ./...\n", "README.md": "# big\n\nA big repository.\n"} {
			if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
				b.Fatal(err)
			}
		}
		b.Run(fmt.Sprintf("%d-files", files), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				r, err := BuildRecon(context.Background(), ReconOptions{Root: root, NoGit: true})
				if err != nil {
					b.Fatal(err)
				}
				if r.Files < files {
					b.Fatalf("%d files surveyed of %d", r.Files, files)
				}
			}
		})
	}
}
