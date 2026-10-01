#!/bin/sh
# Tests for scripts/windows-packages.sh against the real module. Run: sh scripts/windows-packages_test.sh
set -eu
here=$(cd "$(dirname "$0")" && pwd)
cd "$here/.."

passed=0
failed=0
ok() { passed=$((passed + 1)); }
bad() { failed=$((failed + 1)); printf 'FAIL: %s\n' "$1" >&2; }

module=$(go list -m)
all=$(go list ./...)
got=$(sh scripts/windows-packages.sh)
excluded=$(sed 's/#.*//; s/[[:space:]]*$//; /^$/d' scripts/windows-excluded.txt)

# the excluded packages are not in the list, every other package is, once
n_all=$(printf '%s\n' "$all" | wc -l | tr -d ' ')
n_got=$(printf '%s\n' "$got" | wc -l | tr -d ' ')
n_ex=$(printf '%s\n' "$excluded" | wc -l | tr -d ' ')
if [ $((n_got + n_ex)) -eq "$n_all" ]; then ok; else bad "$n_got listed + $n_ex excluded is not the $n_all packages of the module"; fi

printf '%s\n' "$excluded" | while IFS= read -r rel; do
	if printf '%s\n' "$got" | grep -qx -- "$module/$rel"; then echo "$rel"; fi
done > "${TMPDIR:-/tmp}/windows-packages-leaked.$$"
if [ -s "${TMPDIR:-/tmp}/windows-packages-leaked.$$" ]; then bad "an excluded package is listed: $(cat "${TMPDIR:-/tmp}/windows-packages-leaked.$$")"; else ok; fi
rm -f "${TMPDIR:-/tmp}/windows-packages-leaked.$$"

# a package nobody decided about is tested
if printf '%s\n' "$got" | grep -qx -- "$module/internal/tui/cell"; then ok; else bad "internal/tui/cell is not in the list"; fi
# the module path is kept: the output is import paths
if printf '%s\n' "$got" | grep -qv -- "^$module"; then bad "a line is not an import path of $module"; else ok; fi

printf 'windows-packages_test.sh: %d passed, %d failed\n' "$passed" "$failed"
[ "$failed" -eq 0 ]
