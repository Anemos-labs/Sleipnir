#!/bin/sh
# The packages the Windows job of ci.yml tests: every package of the module except those named in scripts/windows-excluded.txt.
# Prints one import path per line, for `go test $(sh scripts/windows-packages.sh)`.
set -eu
cd "$(dirname "$0")/.."

excluded=$(sed 's/#.*//; s/[[:space:]]*$//; /^$/d' scripts/windows-excluded.txt)
module=$(go list -m)

go list ./... | while IFS= read -r pkg; do
	rel=${pkg#"$module"}
	rel=${rel#/}
	[ -n "$rel" ] || rel=.
	if printf '%s\n' "$excluded" | grep -qx -- "$rel"; then
		continue
	fi
	printf '%s\n' "$pkg"
done
