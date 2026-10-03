package probe_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/provider/openairesp"
	"github.com/anemos-labs/sleipnir/internal/provider/probe"
)

// The fixture writes only the final user-message boundary and reads previously
// written boundaries. Extending a message or replacing the previous user turn
// cannot hit, even when the preceding text is unchanged.
func TestProbePreservesResponsesCacheBoundaries(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		t.Run(fmt.Sprintf("cache=%t", enabled), func(t *testing.T) {
			var mu sync.Mutex
			entries := map[string]int{}
			var totalRead, totalInput, repeats int
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var payload struct {
					Instructions string            `json:"instructions"`
					Input        []json.RawMessage `json:"input"`
				}
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				mu.Lock()
				defer mu.Unlock()
				cached, input := 0, 0
				var final string
				for i, raw := range payload.Input {
					var item struct {
						Role string `json:"role"`
					}
					if err := json.Unmarshal(raw, &item); err != nil {
						t.Error(err)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					prefix, err := json.Marshal(payload.Input[:i+1])
					if err != nil {
						t.Error(err)
						w.WriteHeader(http.StatusInternalServerError)
						return
					}
					input = (len(payload.Instructions) + len(prefix)) / 4
					if item.Role == "user" {
						final = payload.Instructions + "\x00" + string(prefix)
						cached = max(cached, entries[final])
					}
				}
				if enabled && input >= 1024 {
					// Reporting deliberately rounds to 128 tokens. These counts
					// cannot establish the engine's cache-write block size.
					entries[final] = input / 128 * 128
				}
				if input >= 1024 {
					if repeats > 0 {
						totalRead += cached
						totalInput += input
					}
					repeats++
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"fixture\",\"model\":\"test\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}],\"usage\":{\"input_tokens\":%d,\"input_tokens_details\":{\"cached_tokens\":%d},\"output_tokens\":1}}}\n\n", input, cached)
			}))
			defer ts.Close()
			client := openairesp.New(openairesp.Config{Name: "boundary-fixture", BaseURL: ts.URL, Auth: openairesp.StaticKey("fixture-token")})
			report, err := probe.Run(context.Background(), probe.Config{Provider: client, Model: "test", CacheKey: true})
			if err != nil {
				t.Fatal(err)
			}
			if err := report.Failure(); err != nil {
				t.Fatal(err)
			}
			findings := report.Findings
			wantHits := 0
			if enabled {
				wantHits = 9
			}
			if findings.CacheRepeats != 9 || findings.CacheRepeatHits != wantHits || findings.CacheWorks != enabled {
				t.Fatalf("cache findings = %+v; want %d of 9 repeat hits", findings, wantHits)
			}
			if findings.CacheGranularity != 0 {
				t.Errorf("message-boundary counts do not establish a token block size: %d", findings.CacheGranularity)
			}
			mu.Lock()
			defer mu.Unlock()
			if repeats != 10 || totalInput == 0 || findings.CacheHitRatio != float64(totalRead)/float64(totalInput) {
				t.Errorf("ratio %v does not match reported repeat usage %d/%d across %d requests", findings.CacheHitRatio, totalRead, totalInput, repeats)
			}
		})
	}
}
