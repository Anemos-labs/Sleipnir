package chatgptauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"time"
)

func base64url(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// LoginIO is what a sign-in needs from the person's side: somewhere to say things, a way to open the browser, and (where there is no
// browser on this machine, a remote shell) a way to take the address the browser was sent to, pasted.
type LoginIO struct {
	Out io.Writer
	// Open opens a URL in the person's browser; nil, or an error, and the person is given the address to open.
	Open func(url string) error
	// Paste, when not nil, is waited on when the browser could not be opened: it returns the address the browser (on another machine) ended on
	// (http://127.0.0.1:PORT/auth/callback?...), which that browser cannot deliver here by itself.
	Paste func() (string, error)
	// Wait bounds the sign-in; zero is five minutes.
	Wait time.Duration
}

// OpenBrowser opens a URL with the system's own opener.
func OpenBrowser(u string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	default:
		cmd = exec.Command("xdg-open", u)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

var clientIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,200}$`)

// Login signs the person in: it opens the browser on the issuer's page, waits for it to come back to a port of this machine, exchanges the
// code and keeps the result. The Store it returns is the sign-in, ready to use.
func Login(ctx context.Context, opts Options, lio LoginIO) (*Store, error) {
	opts.fill()
	if lio.Out == nil {
		lio.Out = io.Discard
	}
	if lio.Wait <= 0 {
		lio.Wait = 5 * time.Minute
	}
	prev, err := readConnection(opts.Path)
	if err != nil {
		return nil, err
	}
	if prev.HostID == "" {
		if prev.HostID, err = newHostID(); err != nil {
			return nil, err
		}
	}
	disc, err := discover(ctx, opts)
	if err != nil {
		return nil, err
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("a port of this machine is needed to receive the sign-in: %w", err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	redirect := fmt.Sprintf("http://127.0.0.1:%d/auth/callback", port)

	state, _ := randomString(32)
	nonce, _ := randomString(32)
	verifier, _ := randomString(32)
	challenge := base64url(sha256Sum(verifier))
	clientID := dynamicClientID
	if prev.ClientID != "" {
		clientID = prev.ClientID
	}
	q := url.Values{
		"client_id": {clientID}, "response_type": {"code"}, "redirect_uri": {redirect}, "scope": {scopes}, "resource": {Resource},
		"state": {state}, "nonce": {nonce}, "code_challenge_method": {"S256"}, "code_challenge": {challenge},
		"ext_agent_host_id": {prev.HostID}, "agent_name_hint": {opts.AppName},
	}
	if prev.Email != "" {
		q.Set("login_hint", prev.Email)
	}
	authURL := disc.AuthorizationEndpoint + "?" + q.Encode()

	got := make(chan callbackResult, 2)
	srv := &http.Server{ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 15 * time.Second}
	srv.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cb, status := parseCallback(r.URL, r.Method, r.Host, port, state)
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'; base-uri 'none'")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if status != http.StatusOK && cb.err == nil {
			w.WriteHeader(status) // not the sign-in's answer (a probe, another path): nothing is learnt from it
			return
		}
		if cb.err != nil {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, page("Sign-in failed", "Go back to the terminal: it says what happened."))
			got <- callbackResult{err: cb.err}
			return
		}
		fmt.Fprint(w, page("Signed in", "You can close this tab and go back to the terminal."))
		got <- callbackResult{code: cb.code, clientID: cb.clientID}
	})
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	fmt.Fprintf(lio.Out, "Sign in with ChatGPT: your browser opens on OpenAI's page. If it does not, open this address:\n\n  %s\n\n", authURL)
	opened := false
	if lio.Open != nil {
		if err := lio.Open(authURL); err != nil {
			fmt.Fprintf(lio.Out, "(the browser could not be opened: %v)\n", err)
		} else {
			opened = true
		}
	}
	if lio.Paste != nil && !opened {
		// With no browser on this machine the redirect cannot arrive by itself (a browser elsewhere cannot reach this port): the address it
		// ends on is pasted. It is only read then, because a read that stays blocked would take the next line the person types.
		fmt.Fprintf(lio.Out, "On a machine with a browser, open it there, and paste here the address the browser ends on (it begins %s).\n", redirect)
		go func() {
			line, err := lio.Paste()
			if err != nil || strings.TrimSpace(line) == "" {
				return
			}
			u, perr := url.Parse(strings.TrimSpace(line))
			if perr != nil {
				got <- callbackResult{err: errors.New("that is not an address")}
				return
			}
			cb, _ := parseCallback(u, http.MethodGet, net.JoinHostPort("127.0.0.1", fmt.Sprint(port)), port, state)
			got <- cb
		}()
	}

	wait, cancel := context.WithTimeout(ctx, lio.Wait)
	defer cancel()
	var cb callbackResult
	select {
	case cb = <-got:
	case <-wait.Done():
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("the sign-in did not come back in time")
	}
	if cb.err != nil {
		return nil, cb.err
	}
	// The first sign-in names the client "dynamic_agent_client", and the callback carries the id it was registered as; a later one
	// keeps the id it has.
	issued := prev.ClientID
	if cb.clientID != "" {
		issued = cb.clientID
	}
	if !clientIDPattern.MatchString(issued) || issued == dynamicClientID {
		return nil, errors.New("the sign-in came back without a client id of its own")
	}
	tr, err := postToken(ctx, opts.HTTP, disc.TokenEndpoint, url.Values{
		"grant_type": {"authorization_code"}, "client_id": {issued}, "code": {cb.code}, "code_verifier": {verifier}, "redirect_uri": {redirect}, "resource": {Resource},
	})
	if err != nil {
		return nil, fmt.Errorf("exchanging the sign-in for a token: %w", err)
	}
	if tr.RefreshToken == "" || tr.IDToken == "" {
		return nil, errors.New("the token answer has no refresh token or no ID token (the scope offline_access was asked for)")
	}
	claims, err := checkIDToken(tr.IDToken, disc.Issuer, issued, nonce, opts.Now())
	if err != nil {
		return nil, fmt.Errorf("the ID token is not acceptable: %w", err)
	}
	if prev.Subject != "" && prev.Subject != claims.Sub {
		fmt.Fprintf(lio.Out, "Note: this is another ChatGPT account than the one that was signed in before (%s).\n", prev.Email)
	}
	c := connection{
		Version: 1, HostID: prev.HostID, ClientID: issued, Subject: claims.Sub, Email: claims.Email, Name: claims.Name, SavedAt: opts.Now(),
		AccessToken: tr.AccessToken, ExpiresAtMS: opts.Now().Add(time.Duration(tr.ExpiresIn) * time.Second).UnixMilli(),
		RefreshToken: tr.RefreshToken, IDToken: tr.IDToken, Scopes: strings.Fields(tr.Scope),
	}
	if err := writeConnection(opts.Path, c); err != nil {
		return nil, fmt.Errorf("keeping the sign-in: %w", err)
	}
	return &Store{opts: opts, c: c, endpoint: disc, refused: map[string]bool{}}, nil
}

func sha256Sum(s string) []byte {
	h := sha256.Sum256([]byte(s))
	return h[:]
}

// callbackResult is what the redirect brought: the code and the id of the client, or why the sign-in failed.
type callbackResult struct {
	code, clientID string
	err            error
}

// parseCallback reads the browser's return. The request must be a GET to this port's own address on the callback path (a page on the web
// that makes the browser call the port is not an answer), with the state that was sent (compared in constant time).
func parseCallback(u *url.URL, method, host string, port int, state string) (callbackResult, int) {
	if method != http.MethodGet || host != fmt.Sprintf("127.0.0.1:%d", port) || u.Path != "/auth/callback" {
		return callbackResult{}, http.StatusNotFound
	}
	q := u.Query()
	if subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(state)) != 1 {
		return callbackResult{}, http.StatusNotFound // not the answer to this sign-in
	}
	if e := q.Get("error"); e != "" {
		msg := "the sign-in was refused: " + clip(e, 80)
		if d := q.Get("error_description"); d != "" {
			msg += " (" + clip(d, 160) + ")"
		}
		return callbackResult{err: errors.New(msg)}, http.StatusBadRequest
	}
	code := q.Get("code")
	if code == "" {
		return callbackResult{err: errors.New("the sign-in came back without a code")}, http.StatusBadRequest
	}
	return callbackResult{code: code, clientID: q.Get("client_id")}, http.StatusOK
}

func page(title, text string) string {
	return "<!doctype html><meta charset=utf-8><title>" + title + "</title><body style=\"font:16px system-ui;margin:3em\"><h1>" + title + "</h1><p>" + text + "</p>"
}

func newHostID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[6] = b[6]&0x0f | 0x40 // version 4
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("urn:uuid:%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// idClaims is what the ID token says about the account.
type idClaims struct {
	Iss   string          `json:"iss"`
	Aud   json.RawMessage `json:"aud"`
	Azp   string          `json:"azp"`
	Sub   string          `json:"sub"`
	Nonce string          `json:"nonce"`
	Exp   float64         `json:"exp"`
	Iat   float64         `json:"iat"`
	Email string          `json:"email"`
	Name  string          `json:"name"`
}

// checkIDToken reads the claims of the ID token and checks the ones that tie it to this sign-in: who issued it, for whom, that it has not
// expired and answers this request's nonce. The signature is not checked: the token came straight from the issuer's token endpoint over
// TLS, and OpenID Connect (core, 3.1.3.7) lets that stand in for it. Nothing here is trusted for more than showing whose account it is
// and noticing that it changed.
func checkIDToken(token, issuer, clientID, nonce string, now time.Time) (idClaims, error) {
	var c idClaims
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return c, errors.New("not a JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || json.Unmarshal(payload, &c) != nil {
		return c, errors.New("the payload is not JSON")
	}
	if strings.TrimRight(c.Iss, "/") != strings.TrimRight(issuer, "/") {
		return c, errors.New("another issuer")
	}
	var aud []string
	if json.Unmarshal(c.Aud, &aud) != nil {
		var one string
		_ = json.Unmarshal(c.Aud, &one)
		aud = []string{one}
	}
	found := false
	for _, a := range aud {
		found = found || a == clientID
	}
	if !found || (c.Azp != "" && c.Azp != clientID) || (len(aud) > 1 && c.Azp != clientID) {
		return c, errors.New("not for this client")
	}
	if c.Sub == "" {
		return c, errors.New("no subject")
	}
	if c.Exp == 0 || now.After(time.Unix(int64(c.Exp), 0).Add(5*time.Second)) {
		return c, errors.New("expired")
	}
	if c.Nonce != nonce {
		return c, errors.New("not the answer to this sign-in (nonce)")
	}
	return c, nil
}
