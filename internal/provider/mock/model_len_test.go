package mock

import (
	"net/http"
	"strings"
	"testing"
)

// A model id far longer than any real one is refused: each streamed chunk would repeat it.
func TestALongModelIdIsRefused(t *testing.T) {
	ts := New(Config{}, func(c *Call) Reply { return Reply{Text: strings.Repeat("x", 10000)} }).Start()
	defer ts.Close()
	body := `{"model":"` + strings.Repeat("m", 100000) + `","stream":true,"messages":[{"role":"user","content":"hello"}]}`
	resp, err := http.Post(ts.URL+"/v1/chat/completions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}
