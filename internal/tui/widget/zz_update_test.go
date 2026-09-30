package widget_test

// The -update flag of the golden files (go test ./internal/tui/widget -update).
//
// The repo's convention is one flag named update per test binary (internal/kv/golden_test.go), and this binary may hold another
// file that defines it, in a package-level variable or in an init function. This file is named to sort last, so that its init
// function runs after every other one of the package: if the flag is there already there is nothing to do, and if not it is
// defined here, which is what the golden files of the signature widgets (testdata/show/) need.

import "github.com/reee344/sleipnir/internal/tui/widget/showtest"

func init() { showtest.RegisterUpdateFlag() }
