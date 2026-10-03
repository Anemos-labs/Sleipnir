//go:build !unix

package fs

import (
	iofs "io/fs"
	"os"
)

// copyOwner leaves ownership unchanged on platforms without the ownership-copy implementation.
func copyOwner(*os.File, iofs.FileInfo) {}
