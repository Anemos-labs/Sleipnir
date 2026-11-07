//go:build unix

package shell

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/tools"
)

// The bash tool refuses such a command before it runs, in every mode. The signal is 0, so a missed match ends nothing.
func TestTheBashToolDoesNotSignalTheHarness(t *testing.T) {
	h := newHarness(t)
	tool, _ := h.reg.Get("bash")
	in, _ := json.Marshal(map[string]string{"command": "pkill -0 -f " + filepath.Base(os.Args[0])})
	res, err := tool.Run(context.Background(), &tools.Call{Input: in, Env: &tools.Env{Cwd: h.root, Perm: perm.AllowAll{}}})
	if err != nil || !res.IsError || !strings.Contains(res.Text, "harness's own process") {
		t.Errorf("result: %+v %v", res, err)
	}
}
