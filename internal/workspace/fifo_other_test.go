//go:build !unix

package workspace

import "os"

func mkfifo(path string) error { return os.WriteFile(path, nil, 0o644) }
