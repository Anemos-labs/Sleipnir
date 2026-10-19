package shell

import (
	"os"
	"strings"
	"testing"
)

func TestPathOptionGoesInFrontOfPATH(t *testing.T) {
	sep := string(os.PathListSeparator)
	got := withPath([]string{"HOME=/h", "Path=/usr/bin"}, []string{"/state/bin"})
	if got[1] != "Path=/state/bin"+sep+"/usr/bin" || len(got) != 2 {
		t.Errorf("an existing PATH (any case): %v", got)
	}
	if got := withPath([]string{"HOME=/h"}, []string{"/a", "/b"}); got[len(got)-1] != "PATH=/a"+sep+"/b" {
		t.Errorf("no PATH: %v", got)
	}
	m := NewManager(Options{BaseEnv: []string{"PATH=/usr/bin"}, Path: []string{"/state/bin"}})
	found := false
	for _, kv := range m.env("a", t.TempDir()) {
		if strings.HasPrefix(kv, "PATH=/state/bin"+sep) {
			found = true
		}
	}
	if !found {
		t.Errorf("the manager's commands lack the extra directory: %v", m.env("a", t.TempDir()))
	}
}
