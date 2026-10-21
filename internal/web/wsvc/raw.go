package wsvc

import (
	"bytes"
	"errors"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/anemos-labs/sleipnir/internal/checkpoint"
	"github.com/anemos-labs/sleipnir/internal/session"
)

// mediaTypes are the files GET /ws/raw serves, by extension: the formats a browser shows by itself (images, video, audio and
// PDF). Everything else is refused: a file of another type is text the page reads through /ws/file, or bytes it has no viewer for.
var mediaTypes = map[string]string{
	".png": "image/png", ".apng": "image/apng", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif",
	".webp": "image/webp", ".avif": "image/avif", ".bmp": "image/bmp", ".ico": "image/x-icon", ".svg": "image/svg+xml",
	".mp4": "video/mp4", ".m4v": "video/mp4", ".webm": "video/webm", ".ogv": "video/ogg", ".mov": "video/quicktime",
	".mp3": "audio/mpeg", ".wav": "audio/wav", ".ogg": "audio/ogg", ".oga": "audio/ogg", ".opus": "audio/ogg",
	".m4a": "audio/mp4", ".aac": "audio/aac", ".flac": "audio/flac",
	".pdf": "application/pdf",
}

// MediaType is the content type GET /ws/raw serves a project path with, and whether it serves it at all.
func MediaType(rel string) (string, bool) {
	t, ok := mediaTypes[strings.ToLower(path.Ext(rel))]
	return t, ok
}

// rawPolicy is the Content-Security-Policy of a served file. The file is shown by the page in an <img>, <video> or <audio>
// element, where none of it runs; opened on its own (a new tab), it is a sandboxed document of an opaque origin that runs no
// script and loads nothing, so an SVG an agent wrote cannot act as the page.
const rawPolicy = "default-src 'none'; img-src data:; style-src 'unsafe-inline'; media-src 'self'; sandbox"

// pdfPolicy is the policy of a served PDF: the page frames it (frame-ancestors 'self'). A browser's PDF viewer does not run in a
// sandboxed document, so a PDF has no sandbox; it still loads nothing of this origin and runs no script of the page.
const pdfPolicy = "default-src 'none'; object-src 'self'; frame-ancestors 'self'"

// handleRaw answers GET /api/sessions/{id}/ws/raw?path=&at=: the bytes of an image, a video, an audio file or a PDF at a point
// (live by default), with its type, for the page's previews. Ranges are honoured, so a video can seek. A path of another type is
// 415, a path the agents may not read 403, like every Workspace route.
func (s *service) handleRaw(w http.ResponseWriter, r *http.Request) {
	_, sess, ok := s.tabOf(w, r)
	if !ok {
		return
	}
	rel, err := s.readable(sess, r.URL.Query().Get("path"))
	if err != nil {
		fail(w, err)
		return
	}
	ctype, ok := MediaType(rel)
	if !ok {
		fail(w, werr(http.StatusUnsupportedMediaType, "not_media", "this file is not an image, a video, an audio file or a PDF: read it as text"))
		return
	}
	at, err := parsePoint(r.URL.Query().Get("at"), "live")
	if err != nil {
		fail(w, err)
		return
	}
	release, err := s.acquire(r.Context())
	if err != nil {
		return
	}
	data, fixed, err := rawAt(sess, rel, at)
	release()
	if err != nil {
		fail(w, err)
		return
	}
	h := w.Header()
	h.Set("Content-Type", ctype)
	if ctype == "application/pdf" {
		h.Set("Content-Security-Policy", pdfPolicy)
		h.Set("X-Frame-Options", "SAMEORIGIN")
	} else {
		h.Set("Content-Security-Policy", rawPolicy)
	}
	// a checkpoint's content never changes; the live file is read again each time (the envelope's no-store stays for it)
	if fixed {
		h.Set("Cache-Control", "private, max-age=3600")
	}
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(data))
}

// rawAt reads the bytes of a file at a point under the checkpoint store's rules (inside the root, no symlink followed, at most
// the store's cap). fixed is true when the content is a checkpoint's, which never changes.
func rawAt(sess *session.Session, rel string, at point) (data []byte, fixed bool, err error) {
	var exists bool
	id := at.cp
	if at.base {
		if list := sess.Ckpt.Infos(); len(list) > 0 {
			id = list[0].ID
		}
	}
	fixed = !at.live && id != ""
	if fixed {
		data, exists, err = sess.Ckpt.ContentAt(id, rel)
	} else { // the base of a session that has no checkpoint yet is the file as it is
		data, exists, err = sess.Ckpt.CurrentContent(rel)
	}
	switch {
	case errors.Is(err, checkpoint.ErrUnknownCheckpoint):
		return nil, false, werr(http.StatusNotFound, "no_checkpoint", "there is no such checkpoint")
	case errors.Is(err, checkpoint.ErrOutsideRoot):
		return nil, false, errBadPath
	case errors.Is(err, checkpoint.ErrTooLarge):
		return nil, false, werr(http.StatusRequestEntityTooLarge, "too_large", "the file is larger than the workspace reads")
	case errors.Is(err, checkpoint.ErrNotRegular), errors.Is(err, checkpoint.ErrNotSaved):
		return nil, false, werr(http.StatusUnprocessableEntity, "not_saved", "the content of this file at that point was not saved")
	case err != nil:
		return nil, false, err
	case !exists:
		return nil, false, errNoFile
	}
	return data, fixed, nil
}
