//go:build !unix

package inspect

import "errors"

func mkfifo(string) error { return errors.New("no FIFOs on this platform") }
