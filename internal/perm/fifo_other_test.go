//go:build !unix

package perm

import "errors"

func mkfifo(string) error { return errors.New("not supported") }
