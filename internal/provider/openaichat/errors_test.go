package openaichat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/reee344/sleipnir/internal/provider"
)

// Hostile characters are built at run time so that none sits in this file.
var (
	bidi = string(rune(0x202e))
	zwsp = string(rune(0x200b))
	tag  = string(rune(0xe0041))
)

const attack = "\x1b]52;c;ZXZpbA==\a\x1b[2J\x1b[31m"

func jsonStr(s string) string { b, _ := json.Marshal(s); return string(b) }

func hostileBody(msg string) string {
	return `{"error":{"message":` + jsonStr(msg) + `,"metadata":{"provider_message":` + jsonStr("upstream"+attack+bidi) + `}}}`
}

func clean(t *testing.T, s string) {
	t.Helper()
	if len(s) > provider.MaxErrorText+256 {
		t.Errorf("text of %d bytes", len(s))
	}
	for _, bad := range []string{"\x1b", "\a", "\n", "\r", bidi, zwsp, tag} {
		if strings.Contains(s, bad) {
			t.Errorf("text carries %q: %q", bad, s[:min(len(s), 120)])
		}
	}
}

func TestHTTPErrorTextIsCappedAndSanitised(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		body   string
	}{
		"json message":             {400, hostileBody("bad request " + attack + bidi + zwsp + tag + strings.Repeat("x", 900_000))},
		"json message, huge":       {500, hostileBody(strings.Repeat("y", 5<<20))},
		"raw body":                 {502, "<html>\x1b[2J" + attack + strings.Repeat("z", 100_000)},
		"raw body with newlines":   {503, "line one\nline two\r\nsleipnir: session ok\n\n" + attack},
		"only controls":            {500, "\x1b\x1b\x1b\a\a"},
		"NUL padded":               {400, `{"error":{"message":"` + strings.Repeat(`\u0000`, 5000) + `real"}}`},
		"message that is an array": {400, `{"error":{"message":["a"]}}`},
	} {
		t.Run(name, func(t *testing.T) {
			pe := mapHTTPError(tc.status, http.Header{}, []byte(tc.body))
			clean(t, pe.Message)
			clean(t, pe.Error())
			if len(pe.Message) > provider.MaxErrorText {
				t.Errorf("message is %d bytes", len(pe.Message))
			}
			if len(pe.Raw) > provider.MaxRawBytes {
				t.Errorf("raw is %d bytes", len(pe.Raw))
			}
			if pe.Status != tc.status {
				t.Errorf("status %d", pe.Status)
			}
		})
	}
}

func TestErrorClassificationSurvivesSanitising(t *testing.T) {
	// Hiding a keyword behind zero-width characters must not dodge the classification.
	msg := "maximum con" + zwsp + "text length is 131072 tokens" + attack
	pe := mapHTTPError(400, http.Header{}, []byte(hostileBody(msg)))
	if pe.Kind != provider.ErrContextLength {
		t.Fatalf("kind = %v", pe.Kind)
	}
	// And ordinary messages come through unchanged.
	pe = mapHTTPError(401, http.Header{}, []byte(`{"error":{"message":"Invalid API key: sk-...abcd"}}`))
	if pe.Kind != provider.ErrAuth || pe.Message != "Invalid API key: sk-...abcd" {
		t.Fatalf("%v %q", pe.Kind, pe.Message)
	}
	pe = mapHTTPError(500, http.Header{}, []byte(`{"error":{"message":"boom","metadata":{"provider_message":"upstream said no"}}}`))
	if pe.Message != "boom (upstream said no)" {
		t.Fatalf("%q", pe.Message)
	}
}

func TestInBandErrorTextIsSanitised(t *testing.T) {
	pe := mapInBandError(&apiError{Code: json.RawMessage("502"), Message: "gateway down" + attack + strings.Repeat("q", 100_000), Metadata: json.RawMessage(`{"x":"` + strings.Repeat("m", 200_000) + `"}`)})
	clean(t, pe.Message)
	if pe.Kind != provider.ErrServer || pe.Status != 502 || len(pe.Raw) > provider.MaxRawBytes {
		t.Fatalf("%+v", pe)
	}
}

func TestErrorsFromTheWireAreSanitisedEndToEnd(t *testing.T) {
	t.Run("http error", func(t *testing.T) {
		s := newScripted(t, func(w http.ResponseWriter, fl http.Flusher, r *http.Request) {
			w.WriteHeader(400)
			fmt.Fprint(w, hostileBody("nope"+attack+strings.Repeat("x", 50_000)))
		})
		_, err := call(t, s.URL, Config{}, nil)
		pe := wantProviderError(t, err, provider.ErrBadRequest, false)
		clean(t, pe.Message)
		clean(t, err.Error())
	})
	t.Run("error frame in a stream", func(t *testing.T) {
		s := newScripted(t, func(w http.ResponseWriter, fl http.Flusher, r *http.Request) {
			sse(w)
			fmt.Fprint(w, frame("partial"), rawFrame(`{"error":{"code":503,"message":`+jsonStr("Provider disconnected"+attack+strings.Repeat("p", 80_000))+`}}`))
		})
		_, err := call(t, s.URL, Config{}, nil)
		pe := wantProviderError(t, err, provider.ErrServer, true)
		clean(t, pe.Message)
		clean(t, err.Error())
	})
	t.Run("error object in a non-streaming body", func(t *testing.T) {
		s := newScripted(t, func(w http.ResponseWriter, fl http.Flusher, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"error":{"code":429,"message":`+jsonStr("slow down"+attack)+`}}`)
		})
		_, err := New(Config{Name: "t", BaseURL: s.URL}).Do(context.Background(), &provider.Request{Prompt: secRevPrompt("hi"), NoStream: true}, nil)
		clean(t, wantProviderError(t, err, provider.ErrRateLimit, true).Message)
	})
	t.Run("unparseable body", func(t *testing.T) {
		s := newScripted(t, func(w http.ResponseWriter, fl http.Flusher, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, "<html>"+attack+strings.Repeat("h", 200_000))
		})
		_, err := New(Config{Name: "t", BaseURL: s.URL}).Do(context.Background(), &provider.Request{Prompt: secRevPrompt("hi"), NoStream: true}, nil)
		pe := wantProviderError(t, err, provider.ErrServer, true)
		clean(t, pe.Message)
		if len(pe.Raw) > provider.MaxRawBytes {
			t.Errorf("raw is %d bytes", len(pe.Raw))
		}
	})
	t.Run("identifiers the server chose", func(t *testing.T) {
		s := newScripted(t, func(w http.ResponseWriter, fl http.Flusher, r *http.Request) {
			sse(w)
			fmt.Fprint(w, rawFrame(`{"id":`+jsonStr("gen"+attack+strings.Repeat("i", 5000))+`,"model":`+jsonStr("m"+bidi+"odel")+`,"provider":`+jsonStr("up"+attack)+`,"choices":[{"delta":{"content":"hi"}}]}`), finish("stop"))
		})
		resp, err := call(t, s.URL, Config{}, nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range []string{resp.ID, resp.Model, resp.Provider, resp.Turn.Model} {
			clean(t, v)
			if len(v) > 256 {
				t.Errorf("identifier of %d bytes", len(v))
			}
		}
		if resp.Model != "model" || resp.Provider != "up" {
			t.Errorf("model %q provider %q", resp.Model, resp.Provider)
		}
	})
}
