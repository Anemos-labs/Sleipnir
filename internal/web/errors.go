package web

import (
	"errors"
	"net/http"

	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// WriteError renders err as an error response for a route handler. A *wire.Error (found with errors.As) keeps its status, code,
// message and detail, so every route package refuses in the same shape. Any other error is answered 500 "internal" with a fixed
// message: the text of an arbitrary error can carry a path, a command line or a credential and is never sent to the page.
func WriteError(w http.ResponseWriter, err error) {
	var we *wire.Error
	if errors.As(err, &we) && we != nil {
		status := we.Status
		if status < 400 || status > 599 {
			status = http.StatusInternalServerError
		}
		ErrorDetail(w, status, we.Code, we.Msg, we.Detail)
		return
	}
	ErrorDetail(w, http.StatusInternalServerError, "internal", "internal error", nil)
}
