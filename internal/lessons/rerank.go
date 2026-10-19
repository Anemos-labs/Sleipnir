package lessons

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/anemos-labs/sleipnir/internal/provider"
)

// Reranker scores documents by their relevance to a query, one score per document, in order. A cross-encoder reads the query and
// each document together, so it orders a handful of candidates better than the cosine of two vectors embedded apart.
type Reranker interface {
	Rerank(ctx context.Context, query string, docs []string) ([]float64, error)
}

// maxRerankResponse bounds the bytes of one rerank answer read.
const maxRerankResponse = 4 << 20

// HTTPReranker calls a POST {base}/rerank in the form Cohere, Jina and the marketplaces share: {"model","query","documents"}
// answered by {"results":[{"index","relevance_score"}]}. It is safe for concurrent use.
type HTTPReranker struct {
	url, apiKey, model string
	headers            map[string]string
	hc                 *http.Client
	keyErr             error

	mu    sync.Mutex
	usage Usage
}

// NewHTTPReranker returns a reranker for model at baseURL, with the key, headers and client rules of NewHTTPEmbedder.
func NewHTTPReranker(baseURL, apiKey, model string, headers map[string]string, client *http.Client) *HTTPReranker {
	e := NewHTTPEmbedder(baseURL, apiKey, model, headers, client) // the same client, key and URL checks
	return &HTTPReranker{url: strings.TrimSuffix(e.url, "/embeddings") + "/rerank", apiKey: apiKey, model: model, headers: headers, hc: e.hc, keyErr: e.keyErr}
}

// TakeUsage returns what the endpoint reported since the last TakeUsage and starts counting again.
func (r *HTTPReranker) TakeUsage() Usage {
	r.mu.Lock()
	defer r.mu.Unlock()
	u := r.usage
	r.usage = Usage{}
	return u
}

// Rerank implements Reranker. No documents is no request; a document the answer leaves out scores 0.
func (r *HTTPReranker) Rerank(ctx context.Context, query string, docs []string) ([]float64, error) {
	if len(docs) == 0 {
		return nil, nil
	}
	if r.keyErr != nil {
		return nil, fmt.Errorf("rerank: %w", r.keyErr)
	}
	body, err := json.Marshal(struct {
		Model     string   `json:"model"`
		Query     string   `json:"query"`
		Documents []string `json:"documents"`
	}{r.model, query, docs})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("rerank: %w", err)
	}
	for k, v := range r.headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if r.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+r.apiKey)
	}
	res, err := r.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("rerank: %w", err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, maxRerankResponse+1))
	if err != nil {
		return nil, fmt.Errorf("rerank: reading the answer: %w", err)
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		msg := provider.SanitizeText(string(raw), 300)
		if msg == "" {
			msg = http.StatusText(res.StatusCode)
		}
		return nil, fmt.Errorf("rerank: HTTP %d: %s", res.StatusCode, msg)
	}
	if len(raw) > maxRerankResponse {
		return nil, fmt.Errorf("rerank: the answer is larger than %d bytes", maxRerankResponse)
	}
	var a struct {
		Results []struct {
			Index int     `json:"index"`
			Score float64 `json:"relevance_score"`
		} `json:"results"`
		Usage struct {
			TotalTokens int      `json:"total_tokens"`
			Cost        *float64 `json:"cost"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, fmt.Errorf("rerank: the answer is not a rerank response: %w", err)
	}
	r.mu.Lock()
	r.usage.Requests++
	r.usage.PromptTokens += a.Usage.TotalTokens
	if a.Usage.Cost != nil && *a.Usage.Cost > 0 {
		r.usage.Cost += *a.Usage.Cost
	}
	r.mu.Unlock()
	scores := make([]float64, len(docs))
	for _, x := range a.Results {
		if x.Index < 0 || x.Index >= len(docs) {
			return nil, fmt.Errorf("rerank: result index %d is out of range", x.Index)
		}
		scores[x.Index] = x.Score
	}
	return scores, nil
}

// Rerank orders candidates by rr's relevance to task, most relevant first (ties keep the candidates' order), and keeps k. A nil
// rr, or k at least the number of candidates with nothing to choose, returns the first k unchanged.
func Rerank(ctx context.Context, rr Reranker, task string, candidates []Lesson, k int) ([]Lesson, error) {
	if k <= 0 {
		return nil, nil
	}
	if rr == nil || len(candidates) <= 1 {
		return candidates[:min(k, len(candidates))], nil
	}
	docs := make([]string, len(candidates))
	for i, l := range candidates {
		docs[i] = "[" + l.Kind + "] " + l.Text
	}
	scores, err := rr.Rerank(ctx, Clean(task, MaxTaskRunes*4), docs)
	if err != nil {
		return nil, err
	}
	order := make([]int, len(candidates))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return scores[order[a]] > scores[order[b]] })
	out := make([]Lesson, 0, min(k, len(candidates)))
	for _, i := range order[:min(k, len(order))] {
		out = append(out, candidates[i])
	}
	return out, nil
}
