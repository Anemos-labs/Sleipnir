//go:build !unix

package fs

import (
	iofs "io/fs"
	"os"
)

func copyOwner(*os.File, iofs.FileInfo) {}
