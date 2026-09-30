//go:build !unix

package checkpoint

import "io/fs"

func ownerOf(fs.FileInfo) *owner { return nil }

func chownPath(string, *owner) {}
