package lessons

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/anemos-labs/sleipnir/internal/provider"
)

// Embedder turns texts into vectors, one per text, in order.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// Bounds of the HTTP embedder.
const (
	embedBatch       = 64       // texts per request
	maxEmbedResponse = 64 << 20 // bytes of one response read
	embedTimeout     = 60 * time.Second
)

// Usage is what an embeddings endpoint reported for the requests it answered.
type Usage struct {
	Requests     int
	PromptTokens int
	Cost         float64 // US dollars, from usage.cost; 0 when the endpoint does not report it
}

// HTTPEmbedder calls an OpenAI-compatible POST {base}/embeddings. It is safe for concurrent use.
type HTTPEmbedder struct {
	url, apiKey, model string
	headers            map[string]string
	hc                 *http.Client
	keyErr             error // why the key must not be sent to this URL

	mu    sync.Mutex
	usage Usage
}

// NewHTTPEmbedder returns an embedder for model at baseURL (the API root, such as https://host/api/v1). apiKey goes in an
// Authorization: Bearer header when it is not empty, and only over https or to a loopback host. headers are added to every
// request. A nil client gets one that honours HTTPS_PROXY and the like, reports a refused proxy CONNECT as such, and times out
// after a minute; any client is wrapped so that redirects stay on the endpoint's origin.
func NewHTTPEmbedder(baseURL, apiKey, model string, headers map[string]string, client *http.Client) *HTTPEmbedder {
	if client == nil {
		client = &http.Client{Timeout: embedTimeout, Transport: &http.Transport{
			Proxy:                  http.ProxyFromEnvironment,
			OnProxyConnectResponse: provider.CheckProxyConnect,
			ForceAttemptHTTP2:      true,
			TLSHandshakeTimeout:    15 * time.Second,
			IdleConnTimeout:        90 * time.Second,
			DialContext:            (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		}}
	}
	base := strings.TrimRight(baseURL, "/")
	e := &HTTPEmbedder{url: base + "/embeddings", apiKey: apiKey, model: model, headers: headers, hc: provider.HardenClient(client)}
	if apiKey != "" {
		e.keyErr = provider.CheckKeyTransport(base)
	}
	return e
}

// Usage returns what the endpoint reported over the life of e.
func (e *HTTPEmbedder) Usage() Usage {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.usage
}

// TakeUsage returns what the endpoint reported since the last TakeUsage and starts counting again, so a caller can add each
// share to a session's spend exactly once.
func (e *HTTPEmbedder) TakeUsage() Usage {
	e.mu.Lock()
	defer e.mu.Unlock()
	u := e.usage
	e.usage = Usage{}
	return u
}

// Embed implements Embedder, in requests of at most 64 texts. No texts is no request.
func (e *HTTPEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if e.keyErr != nil {
		return nil, fmt.Errorf("embeddings: %w", e.keyErr)
	}
	out := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += embedBatch {
		vs, err := e.embed(ctx, texts[start:min(start+embedBatch, len(texts))])
		if err != nil {
			return nil, err
		}
		out = append(out, vs...)
	}
	return out, nil
}

// embedResponse is the part of an embeddings answer that is read.
type embedResponse struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
	Usage struct {
		PromptTokens int      `json:"prompt_tokens"`
		Cost         *float64 `json:"cost"`
	} `json:"usage"`
}

// embed sends one request and returns its vectors in the order of texts.
func (e *HTTPEmbedder) embed(ctx context.Context, texts []string) ([][]float32, error) {
	body, err := json.Marshal(struct {
		Model string   `json:"model"`
		Input []string `json:"input"`
	}{e.model, texts})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("embeddings: %w", err)
	}
	for k, v := range e.headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if e.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.apiKey)
	}
	res, err := e.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embeddings: %w", err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, maxEmbedResponse+1))
	if err != nil {
		return nil, fmt.Errorf("embeddings: reading the answer: %w", err)
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		msg := provider.SanitizeText(string(raw), 300)
		if msg == "" {
			msg = http.StatusText(res.StatusCode)
		}
		return nil, fmt.Errorf("embeddings: HTTP %d: %s", res.StatusCode, msg)
	}
	if len(raw) > maxEmbedResponse {
		return nil, fmt.Errorf("embeddings: the answer is larger than %d bytes", maxEmbedResponse)
	}
	var r embedResponse
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("embeddings: the answer is not an embeddings response: %w", err)
	}
	e.mu.Lock()
	e.usage.Requests++
	e.usage.PromptTokens += r.Usage.PromptTokens
	if r.Usage.Cost != nil && *r.Usage.Cost > 0 {
		e.usage.Cost += *r.Usage.Cost
	}
	e.mu.Unlock()
	if len(r.Data) != len(texts) {
		return nil, fmt.Errorf("embeddings: %d vectors for %d texts", len(r.Data), len(texts))
	}
	out := make([][]float32, len(texts))
	for _, d := range r.Data {
		switch {
		case d.Index < 0 || d.Index >= len(texts):
			return nil, fmt.Errorf("embeddings: vector index %d is out of range", d.Index)
		case out[d.Index] != nil:
			return nil, fmt.Errorf("embeddings: vector index %d appears twice", d.Index)
		case len(d.Embedding) == 0:
			return nil, errors.New("embeddings: an empty vector")
		}
		out[d.Index] = d.Embedding
	}
	return out, nil
}
