//go:build windows

package main

import (
	"errors"
	"os"
)

// holdLock reports that there is no advisory lock here; the tests that need one skip before they call it.
func holdLock(*os.File) error { return errors.New("no advisory lock on this system") }
