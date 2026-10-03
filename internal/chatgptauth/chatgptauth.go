// Package chatgptauth is "Sign in with ChatGPT" for an open-source app (OpenAI's token sharing): the person signs in with a browser, once,
// and the app then calls the Responses API with a token that draws on their ChatGPT plan instead of an API key's billing.
//
// The protocol is the one OpenAI documents (developers.openai.com/siwc, "Integrating Sign in with ChatGPT in your Opensource App"):
// OpenID discovery at the issuer, the authorization-code flow with PKCE (S256) and a loopback redirect on this machine, a dynamic client
// registration (the first sign-in names the client "dynamic_agent_client" and the callback carries the id it was issued, which every
// later sign-in and every refresh uses), a stable id for this installation, and a refresh token that rotates. Nothing here is the Codex
// CLI's own client: this app is registered as itself.
//
// The tokens live in one file, ~/.sleipnir/chatgpt.json (mode 0600, written atomically). Only this package reads it; the tools of a
// session never see a token, and the file is guarded like the other files of the home directory that hold tokens (internal/perm).
package chatgptauth

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// What the issuer and the API are called, as documented.
const (
	Issuer   = "https://auth.openai.com"
	Resource = "https://api.openai.com/v1"

	scopes          = "openid profile email offline_access resource.invoke chatgpt.tokens.use.direct"
	dynamicClientID = "dynamic_agent_client"
	refreshMargin   = 2 * time.Minute // a token that expires sooner than this is renewed before it is used
)

// ErrNotConnected means this machine has no ChatGPT login.
var ErrNotConnected = errors.New("not signed in with ChatGPT: run `sleipnir login chatgpt` (/login chatgpt in the chat)")

// Path is where the connection is kept, under the home directory.
func Path(home string) string { return filepath.Join(home, ".sleipnir", "chatgpt.json") }

// Options says where the connection lives and who the issuer and the API are; the zero value is the real thing, a test points them at
// fakes.
type Options struct {
	Path    string
	Issuer  string // Issuer
	Models  string // the URL of the model list: Resource + "/models"
	AppName string // what the sign-in page calls this app: "Sleipnir"
	HTTP    *http.Client
	Now     func() time.Time
}

// fill supplies missing authentication endpoints, app name, clock, and a timeout-limited HTTP
// client that refuses redirects.
func (o *Options) fill() {
	if o.Issuer == "" {
		o.Issuer = Issuer
	}
	if o.Models == "" {
		o.Models = Resource + "/models"
	}
	if o.AppName == "" {
		o.AppName = "Sleipnir"
	}
	if o.HTTP == nil {
		// no redirects: a token request that is sent somewhere else is a token request sent to an attacker
		o.HTTP = &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	if o.Now == nil {
		o.Now = time.Now
	}
}

// connection is the file. HostID and ClientID are the registration, kept across a sign-out so that signing in again is the same
// installation and the same client; the rest is the sign-in.
type connection struct {
	Version      int       `json:"version"`
	HostID       string    `json:"host_id"`
	ClientID     string    `json:"client_id,omitempty"`
	Subject      string    `json:"subject,omitempty"`
	Email        string    `json:"email,omitempty"`
	Name         string    `json:"name,omitempty"`
	SavedAt      time.Time `json:"saved_at"`
	AccessToken  string    `json:"access_token,omitempty"`
	ExpiresAtMS  int64     `json:"expires_at_ms,omitempty"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	IDToken      string    `json:"id_token,omitempty"`
	Scopes       []string  `json:"scopes,omitempty"`
}

// signedIn reports whether any access or refresh credential is stored; it does not check expiry.
func (c *connection) signedIn() bool { return c.RefreshToken != "" || c.AccessToken != "" }

// expires converts the stored Unix-millisecond expiry to a time value.
func (c *connection) expires() time.Time { return time.UnixMilli(c.ExpiresAtMS) }

// readConnection loads saved authentication state, treats a missing file as empty, and reports
// malformed state with recovery guidance.
func readConnection(path string) (connection, error) {
	var c connection
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return c, nil
		}
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return connection{}, fmt.Errorf("%s is not readable (%v): remove it and sign in again", path, err)
	}
	return c, nil
}

// write keeps the connection where it was, atomically and for its owner only.
func writeConnection(path string, c connection) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".chatgpt-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Store is the sign-in on this machine. It is an openairesp.Authorizer: it hands out the access token and renews it when it is due.
type Store struct {
	opts Options

	mu       sync.Mutex
	c        connection
	endpoint *discovery // the issuer's endpoints, once asked
	refused  map[string]bool
}

// Open reads the connection: ErrNotConnected when there is no sign-in.
func Open(opts Options) (*Store, error) {
	opts.fill()
	c, err := readConnection(opts.Path)
	if err != nil {
		return nil, err
	}
	if !c.signedIn() {
		return nil, ErrNotConnected
	}
	return &Store{opts: opts, c: c, refused: map[string]bool{}}, nil
}

// Connected reports whether there is a sign-in at path, without reading a token into memory.
func Connected(path string) bool {
	c, err := readConnection(path)
	return err == nil && c.signedIn()
}

// Who is the account that is signed in, as the sign-in page gave it.
func (s *Store) Who() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.c.Email != "":
		return s.c.Email
	case s.c.Name != "":
		return s.c.Name
	}
	return s.c.Subject
}

// refreshError is a failed renewal. A final one means the sign-in is over and the person has to sign in again; a temporary one (the
// network, a server that is down) may pass.
type refreshError struct {
	msg       string
	temporary bool
}

// Error returns the saved token-refresh diagnostic.
func (e *refreshError) Error() string { return e.msg }

// Temporary reports whether the refresh failure was classified as retryable.
func (e *refreshError) Temporary() bool { return e.temporary }

// IsSignInError says whether err is the sign-in itself failing (none, or ended): an error that already tells the person to sign in again.
func IsSignInError(err error) bool {
	var re *refreshError
	return errors.Is(err, ErrNotConnected) || errors.As(err, &re)
}

// Token implements openairesp.Authorizer: the access token, renewed first when it expires within two minutes.
func (s *Store) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.usable(s.c) {
		return s.c.AccessToken, nil
	}
	if err := s.refreshLocked(ctx); err != nil {
		// a token that has not expired yet still works while the renewal is not possible for now
		var re *refreshError
		if errors.As(err, &re) && re.temporary && s.c.AccessToken != "" && s.opts.Now().Before(s.c.expires()) && !s.refused[s.c.AccessToken] {
			return s.c.AccessToken, nil
		}
		return "", err
	}
	return s.c.AccessToken, nil
}

// usable rejects absent, previously refused, and near-expiry access tokens.
func (s *Store) usable(c connection) bool {
	return c.AccessToken != "" && !s.refused[c.AccessToken] && s.opts.Now().Add(refreshMargin).Before(c.expires())
}

// Refused implements openairesp.Authorizer: the endpoint refused this token, so the next Token renews.
func (s *Store) Refused(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refused[token] = true
}

// refreshLocked renews the tokens. Another process of this person's may have done it already (the refresh token rotates, so the one
// held here would be refused): the file is read first and again if the renewal is refused, and what is there wins when it is newer.
func (s *Store) refreshLocked(ctx context.Context) error {
	if disk, err := readConnection(s.opts.Path); err == nil && disk.signedIn() && disk.RefreshToken != s.c.RefreshToken && s.usable(disk) {
		s.c = disk
		return nil
	}
	if s.c.RefreshToken == "" || s.c.ClientID == "" {
		return &refreshError{msg: "the ChatGPT sign-in has no refresh token: run `sleipnir login chatgpt` (/login chatgpt in the chat)"}
	}
	d, err := s.discover(ctx)
	if err != nil {
		return &refreshError{msg: "ChatGPT sign-in: " + err.Error(), temporary: true}
	}
	tr, err := s.postToken(ctx, d.TokenEndpoint, url.Values{
		"grant_type": {"refresh_token"}, "client_id": {s.c.ClientID}, "refresh_token": {s.c.RefreshToken}, "resource": {Resource},
	})
	if err != nil {
		var te *tokenError
		if errors.As(err, &te) && !te.temporary {
			// refused: either the sign-in is over, or another process renewed it a moment ago and this refresh token is the old one
			if disk, rerr := readConnection(s.opts.Path); rerr == nil && disk.RefreshToken != "" && disk.RefreshToken != s.c.RefreshToken {
				s.c = disk
				if s.usable(disk) {
					return nil
				}
				return s.refreshLocked(ctx)
			}
			return &refreshError{msg: "the ChatGPT sign-in has ended (" + te.Error() + "): run `sleipnir login chatgpt` (/login chatgpt in the chat)"}
		}
		return &refreshError{msg: "renewing the ChatGPT sign-in: " + err.Error(), temporary: true}
	}
	next := s.c
	next.AccessToken = tr.AccessToken
	next.ExpiresAtMS = s.opts.Now().Add(time.Duration(tr.ExpiresIn) * time.Second).UnixMilli()
	if tr.RefreshToken != "" {
		next.RefreshToken = tr.RefreshToken
	}
	if tr.IDToken != "" {
		next.IDToken = tr.IDToken
	}
	if tr.Scope != "" {
		next.Scopes = strings.Fields(tr.Scope)
	}
	next.SavedAt = s.opts.Now()
	if err := writeConnection(s.opts.Path, next); err != nil {
		return &refreshError{msg: "keeping the renewed ChatGPT sign-in: " + err.Error(), temporary: true}
	}
	s.c = next
	s.refused = map[string]bool{}
	return nil
}

// tokenResponse is what the token endpoint answers, for both grants.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	Scope        string `json:"scope"`
}

// tokenError is a refusal by the token endpoint (or its being out of reach). A temporary one may pass.
type tokenError struct {
	status    int
	code      string
	desc      string
	temporary bool
}

// Error prefers an authentication error code over HTTP status and appends a bounded description
// when available.
func (e *tokenError) Error() string {
	s := fmt.Sprintf("http %d", e.status)
	if e.code != "" {
		s = e.code
	}
	if e.desc != "" {
		s += ": " + clip(e.desc, 200)
	}
	return s
}

// clip replaces ASCII controls with spaces and limits diagnostic text by bytes, potentially
// splitting UTF-8.
func clip(s string, n int) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

// postToken submits a token form using the store's configured HTTP client.
func (s *Store) postToken(ctx context.Context, endpoint string, form url.Values) (*tokenResponse, error) {
	return postToken(ctx, s.opts.HTTP, endpoint, form)
}

func postToken(ctx context.Context, hc *http.Client, endpoint string, form url.Values) (*tokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return nil, &tokenError{desc: scrub(err.Error(), form), temporary: true}
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		te := &tokenError{status: resp.StatusCode, temporary: resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests}
		var e struct {
			Error string `json:"error"`
			Desc  string `json:"error_description"`
		}
		if json.Unmarshal(b, &e) == nil {
			te.code, te.desc = e.Error, e.Desc
		}
		return nil, te
	}
	var tr tokenResponse
	if err := json.Unmarshal(b, &tr); err != nil {
		return nil, &tokenError{status: resp.StatusCode, desc: "the answer is not JSON"}
	}
	if tr.AccessToken == "" || !strings.EqualFold(tr.TokenType, "bearer") || tr.ExpiresIn <= 0 {
		return nil, &tokenError{status: resp.StatusCode, desc: "the answer has no usable access token"}
	}
	return &tr, nil
}

// scrub takes the secrets of a form out of an error text: a transport error can quote the URL, and the form is never in a URL here, but
// the rule is cheap.
func scrub(msg string, form url.Values) string {
	for _, k := range []string{"refresh_token", "code", "code_verifier"} {
		if v := form.Get(k); len(v) > 6 {
			msg = strings.ReplaceAll(msg, v, "[removed]")
		}
	}
	return msg
}

// discovery is the part of the issuer's OpenID metadata that is used.
type discovery struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	RevocationEndpoint    string `json:"revocation_endpoint"`
}

// discover asks the issuer for its endpoints, once. Every endpoint must be on the issuer's own host: a metadata document that sends the
// codes and tokens elsewhere is refused.
func (s *Store) discover(ctx context.Context) (*discovery, error) {
	if s.endpoint != nil {
		return s.endpoint, nil
	}
	d, err := discover(ctx, s.opts)
	if err != nil {
		return nil, err
	}
	s.endpoint = d
	return d, nil
}

func discover(ctx context.Context, o Options) (*discovery, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(o.Issuer, "/")+"/.well-known/openid-configuration", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := o.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("the issuer cannot be reached: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the issuer's metadata answered http %d", resp.StatusCode)
	}
	var d discovery
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&d); err != nil {
		return nil, errors.New("the issuer's metadata is not JSON")
	}
	if strings.TrimRight(d.Issuer, "/") != strings.TrimRight(o.Issuer, "/") {
		return nil, fmt.Errorf("the issuer's metadata names another issuer (%s)", clip(d.Issuer, 80))
	}
	host := hostOf(o.Issuer)
	for name, u := range map[string]string{"authorization": d.AuthorizationEndpoint, "token": d.TokenEndpoint, "revocation": d.RevocationEndpoint} {
		if u == "" && name == "revocation" {
			continue // optional
		}
		if hostOf(u) != host || u == "" {
			return nil, fmt.Errorf("the issuer's %s endpoint is not on its own host", name)
		}
	}
	return &d, nil
}

// hostOf parses a URL and returns its host including port, or empty on parse failure.
func hostOf(u string) string {
	p, err := url.Parse(u)
	if err != nil {
		return ""
	}
	return p.Host
}

// Logout asks the issuer to revoke the refresh token (best effort) and forgets the tokens. The registration, the host id and the client
// id, stays: signing in again is then the same installation.
func (s *Store) Logout(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var revokeErr error
	if s.c.RefreshToken != "" && s.c.ClientID != "" {
		if d, err := s.discover(ctx); err == nil && d.RevocationEndpoint != "" {
			form := url.Values{"token": {s.c.RefreshToken}, "token_type_hint": {"refresh_token"}, "client_id": {s.c.ClientID}}
			req, _ := http.NewRequestWithContext(ctx, http.MethodPost, d.RevocationEndpoint, strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			if resp, err := s.opts.HTTP.Do(req); err != nil {
				revokeErr = err
			} else {
				resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					revokeErr = fmt.Errorf("the issuer answered http %d", resp.StatusCode)
				}
			}
		}
	}
	s.c.AccessToken, s.c.RefreshToken, s.c.IDToken, s.c.ExpiresAtMS, s.c.Scopes = "", "", "", 0, nil
	if err := writeConnection(s.opts.Path, s.c); err != nil {
		return err
	}
	if revokeErr != nil {
		return fmt.Errorf("signed out here, but the issuer was not told to revoke the token (%v)", revokeErr)
	}
	return nil
}

// Model is a model of the plan's list.
type Model struct {
	Slug          string
	DisplayName   string
	ContextWindow int // tokens, when the list says (0 when it does not)
}

// Models lists the models the plan can use, as the API gives them: its answer is {"models":[{slug, display_name, visibility}]}, not the
// {"data":[{id}]} of the public list, and only the ones whose visibility is "list" are meant to be offered.
func (s *Store) Models(ctx context.Context) ([]Model, error) {
	tok, err := s.Token(ctx)
	if err != nil {
		return nil, err
	}
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
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
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
		if m.Visibility == "list" && m.Slug != "" && len(m.Slug) <= 200 {
			out = append(out, Model{Slug: m.Slug, DisplayName: clip(m.DisplayName, 200), ContextWindow: max(m.Window, 0)})
		}
	}
	return out, nil
}

// randomString is n random bytes as unpadded base64url.
func randomString(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64url(b), nil
}
