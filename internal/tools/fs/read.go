package fs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/perm"
	"github.com/reee344/sleipnir/internal/tools"
)

// Read implements the read tool.
type Read struct{}

// Spec implements tools.Tool.
func (Read) Spec() core.ToolSpec {
	return core.ToolSpec{
		Name: "read",
		Description: "Read a file (absolute path, or relative to the working directory). Output is line-numbered; " +
			"by default up to 2000 lines starting at `offset` (1-based), so page large files with offset/limit. " +
			"Very long lines are cut. Images (png, jpg, gif, webp) are returned as images. " +
			"Binary files and directories give an error (use ls for directories). " +
			"Read a file before you edit or overwrite it.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"path":{"type":"string","description":"File to read"},` +
			`"offset":{"type":"integer","description":"First line to read (1-based)"},` +
			`"limit":{"type":"integer","description":"Maximum number of lines"}},` +
			`"required":["path"]}`),
		ReadOnly: true,
	}
}

var imageTypes = map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true}

// sniffImage identifies the four image formats providers accept from the bytes
// themselves; a wrong extension must not produce a wrong media type.
func sniffImage(b []byte) string {
	switch {
	case bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")):
		return "image/png"
	case bytes.HasPrefix(b, []byte("\xff\xd8\xff")):
		return "image/jpeg"
	case bytes.HasPrefix(b, []byte("GIF87a")), bytes.HasPrefix(b, []byte("GIF89a")):
		return "image/gif"
	case len(b) >= 12 && bytes.Equal(b[:4], []byte("RIFF")) && bytes.Equal(b[8:12], []byte("WEBP")):
		return "image/webp"
	}
	return ""
}

// Run implements tools.Tool.
func (Read) Run(ctx context.Context, c *tools.Call) (*tools.Result, error) {
	k := begin(ctx, c, "read")
	var a struct {
		Path   string `json:"path"`
		Offset intArg `json:"offset"`
		Limit  intArg `json:"limit"`
		// FilePath is what other harnesses call the parameter; accepting it saves
		// a model that learned that spelling a failed round trip.
		FilePath string `json:"file_path"`
	}
	if r := k.decode(&a); r != nil {
		return r, nil
	}
	if a.Path == "" {
		a.Path = a.FilePath
	}
	if a.Offset.Set && a.Offset.V < 0 {
		return k.fail("offset must be 1 or more (lines are numbered from 1)"), nil
	}
	if a.Limit.Set && a.Limit.V < 0 {
		return k.fail("limit must be positive"), nil
	}
	canon, disp, msg := k.resolveArg(a.Path)
	if msg != "" {
		return k.fail("%s", msg), nil
	}
	if r := k.authorize("read "+disp, false, perm.RiskLow, canon); r != nil {
		return r, nil
	}

	fi, err := os.Stat(canon)
	if err != nil {
		return k.statFailure(canon, disp, err), nil
	}
	if fi.IsDir() {
		return k.fail("%s is a directory, not a file; use ls to list it", disp), nil
	}
	if !fi.Mode().IsRegular() {
		return k.fail("%s is not a regular file (%s)", disp, fileKind(fi.Mode())), nil
	}
	ranged := a.Offset.Set || a.Limit.Set
	maxRead := k.env.Limits.MaxReadBytes
	if fi.Size() > maxFileBytes {
		return k.fail("%s is %s; read handles files up to %s. Use grep to find what you need, or the shell (head, tail, sed -n) for a slice",
			disp, humanBytes(fi.Size()), humanBytes(maxFileBytes)), nil
	}
	isImageExt := imageTypes[strings.ToLower(filepath.Ext(canon))]
	if maxRead > 0 && fi.Size() > maxRead && (isImageExt || !ranged) {
		if isImageExt {
			return k.fail("image %s is %s, larger than the %s limit", disp, humanBytes(fi.Size()), humanBytes(maxRead)), nil
		}
		return k.fail("%s is %s, larger than the %s limit for a whole-file read; pass offset and limit to read a range, or use grep to find what you need",
			disp, humanBytes(fi.Size()), humanBytes(maxRead)), nil
	}

	data, _, res := k.readFile(canon, disp, maxFileBytes)
	if res != nil {
		return res, nil
	}
	// Record the whole file even for a partial read: the agent has seen this
	// version of the file, and that is what the later staleness check compares.
	k.env.Files.RecordRead(k.env.Agent, canon, data)

	if isImageExt {
		if mt := sniffImage(data); mt != "" {
			return k.imageResult(disp, mt, data), nil
		}
	}
	if i := bytes.IndexByte(data, 0); i >= 0 {
		hint := "use the shell (xxd, file, strings) to inspect it"
		if bytes.HasPrefix(data, []byte{0xFF, 0xFE}) || bytes.HasPrefix(data, []byte{0xFE, 0xFF}) {
			hint = "it looks like UTF-16 text; convert it to UTF-8 first"
		}
		return k.fail("%s is a binary file (%s, NUL byte at offset %d); %s", disp, humanBytes(int64(len(data))), i, hint), nil
	}

	_, body := splitBOM(data)
	if len(body) == 0 {
		return k.ok("(file is empty)"), nil
	}
	total := countLines(body)
	offset := a.Offset.V
	if offset < 1 {
		offset = 1
	}
	if offset > total {
		return k.fail("offset %d is past the end of %s, which has %d lines", offset, disp, total), nil
	}
	limit := a.Limit.V
	if limit < 1 {
		limit = defaultReadLines
	}
	out := renderLines(body, offset, limit, k.outputBudget())
	text := out.text
	if out.last < total {
		text += fmt.Sprintf("\n[showing lines %d-%d of %d; continue with offset=%d]", out.first, out.last, total, out.last+1)
	}
	res2 := k.ok(text)
	res2.Meta = map[string]any{"path": disp, "lines": total, "from": out.first, "to": out.last}
	return res2, nil
}

// outputBudget is how many bytes of body a paged result may use so that
// Env.Finish never has to elide the middle of it. A head-and-tail cut is right
// for command output, but a numbered file range must stay contiguous: the
// model can continue with offset, it cannot recall a hole.
func (k *call) outputBudget() int {
	max := k.env.Limits.MaxOutputChars
	if max <= 0 {
		return 0
	}
	b := max - 256
	if b < max/2 {
		b = max / 2
	}
	return b
}

func (k *call) imageResult(disp, mediaType string, data []byte) *tools.Result {
	if k.env.Blobs == nil {
		return k.fail("cannot return %s as an image: no blob store is configured", disp)
	}
	h, err := k.env.Blobs.Put(data)
	if err != nil {
		return k.fail("cannot store image %s: %s", disp, err)
	}
	res := k.ok(fmt.Sprintf("Image %s (%s, %s)", disp, mediaType, humanBytes(int64(len(data)))))
	res.Blocks = []core.Block{{Kind: core.BlockImage, MediaType: mediaType, MediaRef: string(h)}}
	return res
}

// countLines counts lines the way editors do: a final line without a newline is
// still a line, and a trailing newline does not start a new one.
func countLines(b []byte) int {
	if len(b) == 0 {
		return 0
	}
	n := bytes.Count(b, []byte{'\n'})
	if b[len(b)-1] != '\n' {
		n++
	}
	return n
}

type rendered struct {
	text        string
	first, last int
}

// renderLines formats lines offset.. of body as "%6d\t<line>", stopping after
// limit lines or when the byte budget is used up (always emitting at least one
// line). CR before LF is dropped so CRLF files read like LF files; the edit
// tools put the original endings back.
func renderLines(body []byte, offset, limit, budget int) rendered {
	pos := 0
	for n := 1; n < offset && pos < len(body); n++ {
		i := bytes.IndexByte(body[pos:], '\n')
		if i < 0 {
			pos = len(body)
			break
		}
		pos += i + 1
	}
	var sb strings.Builder
	lineNo := offset
	emitted := 0
	for pos < len(body) && emitted < limit {
		end := bytes.IndexByte(body[pos:], '\n')
		var line []byte
		next := len(body)
		if end < 0 {
			line = body[pos:]
		} else {
			line = body[pos : pos+end]
			next = pos + end + 1
			if n := len(line); n > 0 && line[n-1] == '\r' {
				line = line[:n-1]
			}
		}
		text := displayLine(line)
		if emitted > 0 && budget > 0 && sb.Len()+len(text)+9 > budget {
			break
		}
		if emitted > 0 {
			sb.WriteByte('\n')
		}
		num := strconv.Itoa(lineNo)
		for i := len(num); i < 6; i++ {
			sb.WriteByte(' ')
		}
		sb.WriteString(num)
		sb.WriteByte('\t')
		sb.WriteString(text)
		lineNo++
		emitted++
		pos = next
	}
	return rendered{text: sb.String(), first: offset, last: offset + emitted - 1}
}

// displayLine makes one line safe and bounded for the model: invalid UTF-8
// becomes U+FFFD (the file itself is never modified by reading) and lines over
// maxLineChars are cut with a marker.
func displayLine(line []byte) string {
	s := string(line)
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "\uFFFD")
	}
	if len(s) > maxLineChars {
		if n := utf8.RuneCountInString(s); n > maxLineChars {
			return cutRunes(s, maxLineChars) + fmt.Sprintf("… [line truncated: %d chars]", n)
		}
	}
	return s
}
