package taskgen

import (
	"strings"
	"testing"
)

// A file name holding "{dirs}" stays text of the {files} expansion: it is not expanded again, which would break the quoting.
func TestExpandTemplateIsOnePass(t *testing.T) {
	got := expandTemplate("go test {files}", []hiddenFile{{Path: "a/x{dirs}y_test.go"}}, plan{})
	if strings.Count(got, "'") != 2 || !strings.Contains(got, "x{dirs}y") {
		t.Errorf("expanded to %s", got)
	}
}
