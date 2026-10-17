package chatgptauth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// ErrRenewalDue is what ModelsNoRenewal returns when the access token would have to be renewed before the list could be asked.
var ErrRenewalDue = errors.New("the ChatGPT sign-in is due for renewal: the model list was not asked")

// maxListBytes bounds a model list, as the API answers it and as it is kept on disk.
const maxListBytes = 4 << 20

// Models lists the models the plan can use, as the API gives them: its answer is {"models":[{slug, display_name, visibility}]}, not the
// {"data":[{id}]} of the public list, and only the ones whose visibility is "list" are meant to be offered. The access token is renewed
// first when it is due.
func (s *Store) Models(ctx context.Context) ([]Model, error) {
	tok, err := s.Token(ctx)
	if err != nil {
		return nil, err
	}
	return s.models(ctx, tok)
}

// ModelsNoRenewal is Models with the access token as it is: when the token is due for renewal (or was refused) it asks nothing and
// returns ErrRenewalDue. A caller with a short deadline uses it, because the refresh token rotates: a renewal cut off after the issuer
// answered loses the new refresh token, and the sign-in with it.
func (s *Store) ModelsNoRenewal(ctx context.Context) ([]Model, error) {
	s.mu.Lock()
	tok, ok := s.c.AccessToken, s.usable(s.c)
	s.mu.Unlock()
	if !ok {
		return nil, ErrRenewalDue
	}
	return s.models(ctx, tok)
}

// models asks the model list with the access token tok and keeps the entries that are offered (see Models).
func (s *Store) models(ctx context.Context, tok string) ([]Model, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.opts.Models, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/json")
	resp, err := s.opts.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, maxListBytes))
	if resp.StatusCode == http.StatusUnauthorized {
		s.Refused(tok)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the model list answered http %d: %s", resp.StatusCode, clip(string(bytes.TrimSpace(b)), 200))
	}
	var list struct {
		Models []struct {
			Slug        string `json:"slug"`
			DisplayName string `json:"display_name"`
			Visibility  string `json:"visibility"`
			Window      int    `json:"context_window"`
		} `json:"models"`
	}
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, errors.New("the model list is not JSON")
	}
	var out []Model
	for _, m := range list.Models {
		if m.Visibility == "list" {
			if e, ok := listed(m.Slug, m.DisplayName, m.Window); ok {
				out = append(out, e)
			}
		}
	}
	return out, nil
}

// listed is one entry of a model list as it may be used: a slug of at most 200 bytes, a display name clipped to 200, a window that is
// not negative. ok is false for an entry with no usable slug.
func listed(slug, name string, window int) (Model, bool) {
	if slug == "" || len(slug) > 200 {
		return Model{}, false
	}
	return Model{Slug: slug, DisplayName: clip(name, 200), ContextWindow: max(window, 0)}, true
}

// ListPath is where the model list this store asks is kept under dir: chatgpt-models-<the first 12 hex digits of the SHA-256 of the
// list's address and the account>.json. Both are in the name because the list differs between them (another plan, another endpoint).
func (s *Store) ListPath(dir string) string {
	s.mu.Lock()
	account := s.c.Subject
	if account == "" {
		account = s.c.Email
	}
	s.mu.Unlock()
	sum := sha256.Sum256([]byte(s.opts.Models + "\n" + account))
	return filepath.Join(dir, "chatgpt-models-"+hex.EncodeToString(sum[:6])+".json")
}

// listFile is a kept model list: slugs, names and windows, never a token.
type listFile struct {
	Version int        `json:"version"`
	Models  []listItem `json:"models"`
}

// listItem is one model of a kept list.
type listItem struct {
	Slug          string `json:"slug"`
	DisplayName   string `json:"display_name,omitempty"`
	ContextWindow int    `json:"context_window,omitempty"`
}

// SaveList keeps ms at path (ListPath), atomically and readable by its owner only, creating the directory when it is missing. What is
// kept is each model's slug, display name and context window.
func SaveList(path string, ms []Model) error {
	f := listFile{Version: 1, Models: make([]listItem, 0, len(ms))}
	for _, m := range ms {
		f.Models = append(f.Models, listItem{Slug: m.Slug, DisplayName: m.DisplayName, ContextWindow: m.ContextWindow})
	}
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return writeFileAtomic(path, append(b, '\n'), ".chatgpt-models-*.tmp")
}

// LoadList reads a list SaveList kept at path, with the time it was kept (the file's modification time). The file is not trusted more
// than the API: it is read up to the size an answer may have, and an entry that Models would not have kept is left out.
func LoadList(path string) ([]Model, time.Time, error) {
	fh, err := os.Open(path)
	if err != nil {
		return nil, time.Time{}, err
	}
	defer fh.Close()
	st, err := fh.Stat()
	if err != nil {
		return nil, time.Time{}, err
	}
	b, err := io.ReadAll(io.LimitReader(fh, maxListBytes+1))
	if err != nil {
		return nil, time.Time{}, err
	}
	if len(b) > maxListBytes {
		return nil, time.Time{}, fmt.Errorf("%s is larger than a model list may be", path)
	}
	var f listFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, time.Time{}, fmt.Errorf("%s is not a model list", path)
	}
	var out []Model
	for _, m := range f.Models {
		if e, ok := listed(m.Slug, m.DisplayName, m.ContextWindow); ok {
			out = append(out, e)
		}
	}
	return out, st.ModTime(), nil
}
