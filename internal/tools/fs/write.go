package fs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	iofs "io/fs"
	"os"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/tools"
)

// Write implements the write tool.
type Write struct{}

// Spec implements tools.Tool.
func (Write) Spec() core.ToolSpec {
	return core.ToolSpec{
		Name: "write",
		Description: "Create a file or overwrite it with `content`; parent directories are created. " +
			"Overwriting requires that you read the file first and that it has not changed since. " +
			"For changes to an existing file prefer edit or apply_patch, which send only the change. " +
			"Writes are atomic and keep the file's mode; an overwritten file keeps its BOM and CRLF line endings.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"path":{"type":"string","description":"File to write"},` +
			`"content":{"type":"string","description":"Complete new file content"}},` +
			`"required":["path","content"]}`),
	}
}

// Run implements tools.Tool.
func (Write) Run(ctx context.Context, c *tools.Call) (*tools.Result, error) {
	k := begin(ctx, c, "write")
	var a struct {
		Path     string  `json:"path"`
		Content  *string `json:"content"`
		FilePath string  `json:"file_path"` // alias, see Read
	}
	if r := k.decode(&a); r != nil {
		return r, nil
	}
	if a.Path == "" {
		a.Path = a.FilePath
	}
	if a.Content == nil {
		return k.fail(`content is required (use "" to create an empty file)`), nil
	}
	canon, disp, msg := k.resolveArg(a.Path)
	if msg != "" {
		return k.fail("%s", msg), nil
	}
	if r := k.unread(canon, disp); r != nil {
		return r, nil
	}
	if r := k.authorize("write "+disp, true, perm.RiskMedium, canon); r != nil {
		return r, nil
	}

	unlock := fileLocks.acquire(canon)
	defer unlock()

	content := []byte(*a.Content)
	var existing iofs.FileInfo
	var notes []string
	fi, err := os.Stat(canon)
	switch {
	case err == nil:
		if fi.IsDir() {
			return k.fail("%s is a directory; give a file path", disp), nil
		}
		if !fi.Mode().IsRegular() {
			return k.fail("%s is not a regular file (%s)", disp, fileKind(fi.Mode())), nil
		}
		current, _, res := k.readFile(canon, disp, maxFileBytes)
		if res != nil {
			return res, nil
		}
		if r := k.freshness(canon, disp, current, true); r != nil {
			return r, nil
		}
		content, notes = matchExistingStyle(current, content)
		if bytes.Equal(current, content) {
			return k.ok(fmt.Sprintf("No change: %s already has exactly this content.", disp)), nil
		}
		existing = fi
	case errors.Is(err, iofs.ErrNotExist):
		// Creating a file needs no prior read.
	default:
		return k.fail("cannot stat %s: %s", disp, osReason(err)), nil
	}

	if r := k.commit(canon, disp, content, existing); r != nil {
		return r, nil
	}
	verb := "Created"
	tail := ""
	if existing != nil {
		verb = "Overwrote"
		tail = fmt.Sprintf(", was %d", existing.Size())
	}
	text := fmt.Sprintf("%s %s (%s, %s%s)", verb, disp, plural(countLines(content), "line"), plural(len(content), "byte"), tail)
	for _, n := range notes {
		text += "; " + n
	}
	res := k.ok(text)
	res.Meta = map[string]any{"path": disp, "created": existing == nil, "bytes": len(content)}
	return res, nil
}

// matchExistingStyle re-applies the BOM and CRLF line endings of the file being
// overwritten. read hides both (it strips the CR and the mark), so a model that
// rewrites a whole file cannot know they were there; without this an overwrite
// would turn every line of a Windows-style file into a diff.
func matchExistingStyle(existing, content []byte) ([]byte, []string) {
	var notes []string
	bom, body := splitBOM(existing)
	if bom != nil && !bytes.HasPrefix(content, utf8BOM) {
		content = append(append(make([]byte, 0, len(content)+3), utf8BOM...), content...)
		notes = append(notes, "kept the UTF-8 BOM")
	}
	crlf, lf := eolCountsBytes(body)
	if crlf > lf && bytes.IndexByte(content, '\r') < 0 && bytes.IndexByte(content, '\n') >= 0 {
		content = bytes.ReplaceAll(content, []byte("\n"), []byte("\r\n"))
		notes = append(notes, "kept CRLF line endings")
	}
	return content, notes
}

func eolCountsBytes(b []byte) (crlf, lf int) {
	for i := 0; ; {
		j := bytes.IndexByte(b[i:], '\n')
		if j < 0 {
			return
		}
		j += i
		if j > 0 && b[j-1] == '\r' {
			crlf++
		} else {
			lf++
		}
		i = j + 1
	}
}
