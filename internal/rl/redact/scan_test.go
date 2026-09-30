package redact

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// exploratory: scan the repo for redactions
func TestScanRepo(t *testing.T) {
	root := "../../../.."
	if os.Getenv("SCAN_REPO") == "" {
		t.Skip()
	}
	r := New(Config{Salt: "x"})
	filepath.Walk(filepath.Join(root, "Sleipnir"), func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			if info != nil && info.IsDir() && (info.Name() == ".git" || info.Name() == "blobs") {
				return filepath.SkipDir
			}
			return nil
		}
		if info.Size() > 2<<20 {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		s := string(b)
		for i, line := range strings.Split(s, "\n") {
			out, changed := r.Changed(line)
			if changed {
				fmt.Printf("%s:%d\n   %s\n   %s\n", strings.TrimPrefix(p, root), i+1, truncate(line), truncate(out))
			}
		}
		return nil
	})
	fmt.Println(r.Stats())
}
