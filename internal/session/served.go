package session

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/anemos-labs/sleipnir/internal/cost"
	"github.com/anemos-labs/sleipnir/internal/provider"
)

// servedTimeout bounds asking a server of this machine what window it serves a model with, all its questions together.
const servedTimeout = 3 * time.Second

// maxServedBytes bounds one answer of a local server.
const maxServedBytes = 4 << 20

// servedWindow is the context window the server of this machine at base says it serves model id with (askServed), 0 when it says
// none. Each server is asked about each model once in a session.
func (s *Session) servedWindow(ctx context.Context, base, id string) int {
	key := base + " " + id
	if n, ok := s.served[key]; ok {
		return n
	}
	n := askServed(ctx, nil, base, id)
	if s.served == nil {
		s.served = map[string]int{}
	}
	s.served[key] = n
	return n
}

// askServed asks the server at base, a server of this machine whose catalogue gives no window, what window it serves model id
// with: the window a request gets, never the model's maximum (a server that loaded a 128k model with 8k serves 8k, and cuts the
// rest off). Each kind of server says it in its own place, so each is asked in turn, and one that is not that server answers 404:
//
//   - llama.cpp's server: GET /props, default_generation_settings.n_ctx (the context of one slot).
//   - LM Studio: GET /api/v1/models, the context_length of the loaded instance of the model; a model not loaded says nothing,
//     since LM Studio loads it on the first request with its own default.
//   - Ollama: POST /api/show, num_ctx of the model's parameters (what every request that does not set it is served with, at most
//     the model's own context_length); else GET /api/ps, the context_length the running model was loaded with.
//
// vLLM and SGLang give the window in their catalogue (max_model_len), and are not asked. 0 when no server says, or what it says
// is not a window (cost.MaxWindowTokens). The requests carry no key, and stay at base's origin.
func askServed(ctx context.Context, hc *http.Client, base, id string) int {
	if hc == nil {
		hc = &http.Client{}
	}
	hc = provider.HardenClient(hc)
	ctx, cancel := context.WithTimeout(ctx, servedTimeout)
	defer cancel()
	root := strings.TrimSuffix(strings.TrimRight(base, "/"), "/v1")
	for _, ask := range []func(context.Context, *http.Client, string, string) int{llamaCppServed, lmStudioServed, ollamaServed} {
		if n := ask(ctx, hc, root, id); n > 0 && n <= cost.MaxWindowTokens {
			return n
		}
		if ctx.Err() != nil {
			break
		}
	}
	return 0
}

// llamaCppServed is the context of one slot of the llama.cpp server at root.
func llamaCppServed(ctx context.Context, hc *http.Client, root, _ string) int {
	var props struct {
		Settings struct {
			NCtx int `json:"n_ctx"`
		} `json:"default_generation_settings"`
	}
	if !askJSON(ctx, hc, http.MethodGet, root+"/props", nil, &props) {
		return 0
	}
	return props.Settings.NCtx
}

// lmStudioServed is the context length of the instance of model id that LM Studio at root has loaded: the instance of that id,
// else the first instance of the model whose key is id.
func lmStudioServed(ctx context.Context, hc *http.Client, root, id string) int {
	var list struct {
		Models []struct {
			Key       string `json:"key"`
			Instances []struct {
				ID     string `json:"id"`
				Config struct {
					ContextLength int `json:"context_length"`
				} `json:"config"`
			} `json:"loaded_instances"`
		} `json:"models"`
	}
	if !askJSON(ctx, hc, http.MethodGet, root+"/api/v1/models", nil, &list) {
		return 0
	}
	for _, m := range list.Models {
		for _, in := range m.Instances {
			if in.ID == id {
				return in.Config.ContextLength
			}
		}
	}
	for _, m := range list.Models {
		if m.Key == id && len(m.Instances) > 0 {
			return m.Instances[0].Config.ContextLength
		}
	}
	return 0
}

// ollamaServed is the window Ollama at root serves model id with: num_ctx of its parameters, at most the model's own
// context_length; else the context_length it is loaded with now.
func ollamaServed(ctx context.Context, hc *http.Client, root, id string) int {
	var show struct {
		Parameters string         `json:"parameters"`
		Info       map[string]any `json:"model_info"`
	}
	body, _ := json.Marshal(map[string]string{"model": id})
	if !askJSON(ctx, hc, http.MethodPost, root+"/api/show", body, &show) {
		return 0 // not Ollama, or a model it does not have
	}
	if n := numCtx(show.Parameters); n > 0 {
		if arch, _ := show.Info["general.architecture"].(string); arch != "" {
			if trained, ok := show.Info[arch+".context_length"].(float64); ok && trained > 0 && trained < float64(n) {
				return int(trained) // Ollama serves no more than the model was trained for
			}
		}
		return n
	}
	var ps struct {
		Models []struct {
			Name          string `json:"name"`
			Model         string `json:"model"`
			ContextLength int    `json:"context_length"`
		} `json:"models"`
	}
	if !askJSON(ctx, hc, http.MethodGet, root+"/api/ps", nil, &ps) {
		return 0
	}
	for _, m := range ps.Models {
		if m.Name == id || m.Model == id || m.Name == id+":latest" {
			return m.ContextLength
		}
	}
	return 0
}

// numCtx is the num_ctx of an Ollama model's parameters (one "name value" per line), 0 when they set none.
func numCtx(parameters string) int {
	for _, line := range strings.Split(parameters, "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[0] == "num_ctx" {
			if n, err := strconv.Atoi(f[1]); err == nil && n > 0 {
				return n
			}
		}
	}
	return 0
}

// askJSON sends one request to a server of this machine and decodes its 200 answer into out; false when there is no such answer.
func askJSON(ctx context.Context, hc *http.Client, method, url string, body []byte, out any) bool {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rd)
	if err != nil {
		return false
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := hc.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	b, err := provider.ReadCapped(resp.Body, maxServedBytes)
	return err == nil && json.Unmarshal(b, out) == nil
}
