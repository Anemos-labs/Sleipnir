package taskgen

import "strings"

// licenseFiles are the file names searched, in order, for a repository's licence.
var licenseFiles = []string{"LICENSE", "LICENSE.md", "LICENSE.txt", "LICENCE", "COPYING", "COPYING.md", "UNLICENSE"}

// DetectLicense maps licence text to an SPDX identifier by recognising the
// standard wording. It is deliberately conservative: an unrecognised text yields
// "" (unknown), which the licence filter treats as not allowed.
func DetectLicense(text string) string {
	t := strings.ToLower(strings.Join(strings.Fields(text), " "))
	has := func(s string) bool { return strings.Contains(t, s) }
	switch {
	case has("gnu affero general public license"):
		return "AGPL-3.0"
	case has("gnu lesser general public license"):
		if has("version 2.1") {
			return "LGPL-2.1"
		}
		return "LGPL-3.0"
	case has("gnu general public license"):
		if has("version 2") && !has("version 3") {
			return "GPL-2.0"
		}
		return "GPL-3.0"
	case has("mozilla public license") && has("2.0"):
		return "MPL-2.0"
	case has("apache license") && has("version 2.0"):
		return "Apache-2.0"
	case has("this is free and unencumbered software released into the public domain"):
		return "Unlicense"
	case has("cc0 1.0 universal") || has("creative commons zero"):
		return "CC0-1.0"
	case has("permission to use, copy, modify, and/or distribute this software for any purpose with or without fee"):
		return "ISC"
	case has("redistribution and use in source and binary forms"):
		if has("neither the name of") {
			return "BSD-3-Clause"
		}
		return "BSD-2-Clause"
	case has("permission is hereby granted, free of charge, to any person obtaining a copy"):
		return "MIT"
	}
	return ""
}
