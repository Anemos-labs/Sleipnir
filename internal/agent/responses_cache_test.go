package agent_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/provider/openairesp"
)

// Inspect the actual Responses input, not the provider-neutral prompt. An
// implicit cache write ends at the last eligible input item; removing a hot
// message on the next request loses that entry even if all earlier text survives.
func TestResponsesCacheBoundariesSurviveToolLoop(t *testing.T) {
	var previous []json.RawMessage
	requests := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Input []json.RawMessage `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		requests++
		if len(body.Input) < len(previous) {
			t.Errorf("request %d removed previously cached input items", requests)
		} else {
			for i := range previous {
				if string(previous[i]) != string(body.Input[i]) {
					t.Errorf("request %d changed cached item %d: previous message boundary was lost", requests, i)
				}
			}
		}
		previous = body.Input
		w.Header().Set("Content-Type", "application/json")
		output := fmt.Sprintf(`[{"type":"function_call","id":"fc_%d","call_id":"call_%d","name":"work","arguments":"{}"}]`, requests, requests)
		if requests == 7 {
			output = `[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}]`
		}
		io.WriteString(w, `{"id":"response","status":"completed","model":"test","output":`+output+`}`)
	}))
	defer ts.Close()
	prov := openairesp.New(openairesp.Config{BaseURL: ts.URL, Auth: openairesp.StaticKey("")})
	a, _ := cxAgent(t, cxOpts{prov: prov, model: "mock-1", hot: cxBoard(), noCompct: true})
	res, err := a.Run(context.Background(), "do the work")
	if err != nil || res.Steps != 7 {
		t.Fatalf("run: %+v, %v", res, err)
	}
}
