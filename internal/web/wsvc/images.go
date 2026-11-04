package wsvc

import (
	"bytes"
	"encoding/base64"
	"errors"
	"net/http"
	"time"

	"github.com/anemos-labs/sleipnir/internal/agent"
	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/web"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// MaxImageUpload is the largest picture POST /images stores (before base64), and imageUploadBody the body that carries it.
const (
	MaxImageUpload  = 20 << 20
	imageUploadBody = MaxImageUpload/3*4 + 4096
)

// registerImages adds the routes of the pictures a person attaches to a message: POST stores one in the session's blob
// store and answers the token the message carries (agent.ImageToken); GET serves a stored picture back for the transcript.
func (s *service) registerImages(srv *web.Server) {
	srv.HandleFunc("POST /api/sessions/{id}/images", s.handleImageUpload, web.RouteOpts{MaxBody: imageUploadBody, WriteTimeout: 60 * time.Second})
	srv.HandleFunc("GET /api/sessions/{id}/images/{ref}", s.handleImage, web.RouteOpts{})
}

// handleImageUpload answers POST /api/sessions/{id}/images {name, data}: data is the picture in base64 (PNG, JPEG, GIF or
// WebP, at most MaxImageUpload bytes). The answer names the blob and the token to put in the message.
func (s *service) handleImageUpload(w http.ResponseWriter, r *http.Request) {
	_, sess, ok := s.tabOf(w, r)
	if !ok {
		return
	}
	var body wire.ImageUpload
	if !web.DecodeJSON(w, r, &body) {
		return
	}
	data, err := base64.StdEncoding.DecodeString(body.Data)
	if err != nil {
		fail(w, werr(http.StatusBadRequest, "bad_request", "the picture is not base64"))
		return
	}
	if len(data) > MaxImageUpload {
		fail(w, werr(http.StatusRequestEntityTooLarge, "too_large", "the picture is larger than 20 MB"))
		return
	}
	mt := imageType(data)
	if mt == "" {
		fail(w, werr(http.StatusUnsupportedMediaType, "not_image", "only PNG, JPEG, GIF and WebP pictures can be attached"))
		return
	}
	if sess.Blobs == nil {
		fail(w, werr(http.StatusConflict, "busy", "the session is starting: try again in a moment"))
		return
	}
	h, err := sess.Blobs.Put(data)
	if err != nil {
		fail(w, err)
		return
	}
	_ = web.WriteJSON(w, http.StatusOK, wire.ImageStored{Ref: string(h), Type: mt, Bytes: len(data), Token: agent.ImageToken(h, body.Name)})
}

// handleImage answers GET /api/sessions/{id}/images/{ref}: a picture of the session's blob store, served like a workspace
// image (sandboxed, loading nothing). A blob that is not a picture is 404, so the route serves nothing else of the store.
func (s *service) handleImage(w http.ResponseWriter, r *http.Request) {
	_, sess, ok := s.tabOf(w, r)
	if !ok {
		return
	}
	ref := core.Hash(r.PathValue("ref"))
	if !events.ValidHash(ref) || sess.Blobs == nil {
		fail(w, werr(http.StatusNotFound, "not_found", "no such picture"))
		return
	}
	data, err := sess.Blobs.GetMax(ref, MaxImageUpload)
	mt := ""
	if err == nil {
		mt = imageType(data)
	}
	if mt == "" {
		if err != nil && !errors.Is(err, events.ErrBlobNotFound) && !errors.Is(err, events.ErrBlobTooLarge) {
			fail(w, err)
			return
		}
		fail(w, werr(http.StatusNotFound, "not_found", "no such picture"))
		return
	}
	h := w.Header()
	h.Set("Content-Type", mt)
	h.Set("Content-Security-Policy", rawPolicy)
	h.Set("Cache-Control", "private, max-age=86400, immutable") // content addressed: the bytes of a ref never change
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(data))
}

// imageType names the picture format of data, or "" for anything else.
func imageType(b []byte) string {
	switch {
	case len(b) >= 8 && string(b[:8]) == "\x89PNG\r\n\x1a\n":
		return "image/png"
	case len(b) >= 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF:
		return "image/jpeg"
	case len(b) >= 6 && (string(b[:6]) == "GIF87a" || string(b[:6]) == "GIF89a"):
		return "image/gif"
	case len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return "image/webp"
	}
	return ""
}
