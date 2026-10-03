//go:build !unix

package checkpoint

import "io/fs"

// ownerOf returns no ownership metadata on platforms without ownership support.
func ownerOf(fs.FileInfo) *owner { return nil }

// chownPath leaves ownership unchanged on platforms without ownership support.
func chownPath(string, *owner) {}
