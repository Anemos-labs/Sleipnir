package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// TestWriteError checks that a *wire.Error keeps its status, code, message and detail (also when wrapped), that an out-of-range
// status becomes 500, and that any other error is answered with a fixed message that does not repeat the error text.
func TestWriteError(t *testing.T) {
	secret := "/home/alice/.sleipnir/auth.json sk-live-123"
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
		wantMsg    string
		wantDetail bool
	}{
		{"wire error", &wire.Error{Status: 409, Msg: "answer too soon", Code: "too_soon", Detail: map[string]any{"retryAfterMs": 120}}, 409, "too_soon", "answer too soon", true},
		{"wrapped wire error", fmt.Errorf("route: %w", &wire.Error{Status: 404, Msg: "no such tab", Code: "no_tab"}), 404, "no_tab", "no such tab", false},
		{"status below 400", &wire.Error{Status: 200, Msg: "x", Code: "bad_status"}, 500, "bad_status", "x", false},
		{"status above 599", &wire.Error{Status: 700, Msg: "x", Code: "bad_status"}, 500, "bad_status", "x", false},
		{"arbitrary error", errors.New(secret), 500, "internal", "internal error", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			WriteError(rec, tc.err)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status %d, want %d", rec.Code, tc.wantStatus)
			}
			if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
				t.Fatalf("content type %q", got)
			}
			if got := rec.Header().Get("Cache-Control"); got != "no-store" {
				t.Fatalf("cache control %q", got)
			}
			var body struct {
				Error  string          `json:"error"`
				Code   string          `json:"code"`
				Detail json.RawMessage `json:"detail"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("body is not JSON: %v: %s", err, rec.Body.String())
			}
			if body.Code != tc.wantCode || body.Error != tc.wantMsg {
				t.Fatalf("body %+v, want code %q message %q", body, tc.wantCode, tc.wantMsg)
			}
			if (len(body.Detail) > 0) != tc.wantDetail {
				t.Fatalf("detail %q, want present=%v", body.Detail, tc.wantDetail)
			}
			if strings.Contains(rec.Body.String(), "alice") || strings.Contains(rec.Body.String(), "sk-live") {
				t.Fatalf("the response repeats the error text of an arbitrary error: %s", rec.Body.String())
			}
		})
	}
	if got := http.StatusText(500); got == "" {
		t.Fatal("unreachable: keeps net/http imported for the status names")
	}
}
