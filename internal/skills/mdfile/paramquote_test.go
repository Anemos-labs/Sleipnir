package mdfile

import "testing"

// Inside "${x:-...}" a double quote opens a nested double-quoted part instead of closing the outer one: an argument there is
// escaped for double quotes, not single-quoted where the quotes would be plain characters and $(...) would run.
func TestArgsShellFollowsQuotesNestedInParameterExpansion(t *testing.T) {
	for tmpl, want := range map[string]string{
		`echo "${x:-"$1"}"`:    `echo "${x:-"\$(echo EXEC)"}"`,
		`echo "${x:-'$1'}"`:    `echo "${x:-'\$(echo EXEC)'}"`,
		`echo "${x:-"a"}" $1`:  `echo "${x:-"a"}" '$(echo EXEC)'`,
		`echo "${x:-{}}" "$1"`: `echo "${x:-{}}" "\$(echo EXEC)"`,
	} {
		if got, _, err := SubstituteMax(tmpl, `'$(echo EXEC)'`, ArgsShell, 0); err != nil || got != want {
			t.Errorf("%s -> %s, %v; want %s", tmpl, got, err, want)
		}
	}
}
