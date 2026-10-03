package chatgptauth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// issuer is a fake OpenAI: the discovery document, an authorization endpoint (which the test's "browser" calls by hand), a token endpoint
// that checks what the protocol says it must (the PKCE challenge, the client id it issued, a refresh token that rotates), a revocation
// endpoint and a model list.
type issuer struct {
	t  *testing.T
	ts *httptest.Server

	mu          sync.Mutex
	challenge   string // of the last authorization request
	nonce       string
	authQuery   url.Values
	refresh     string // the refresh token that is current
	refreshes   int32
	revoked     []string
	clientID    string
	expiresIn   int
	failRefresh string // an error code the refresh grant answers with
	idClaims    func(map[string]any)
}

func newIssuer(t *testing.T) *issuer {
	is := &issuer{t: t, clientID: "client_abc123", expiresIn: 3600}
	mux := http.NewServeMux()
	is.ts = httptest.NewServer(mux)
	t.Cleanup(is.ts.Close)
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"issuer": is.ts.URL, "authorization_endpoint": is.ts.URL + "/authorize",
			"token_endpoint": is.ts.URL + "/token", "jwks_uri": is.ts.URL + "/jwks", "revocation_endpoint": is.ts.URL + "/revoke"})
	})
	mux.HandleFunc("/token", is.token)
	mux.HandleFunc("/revoke", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		is.mu.Lock()
		is.revoked = append(is.revoked, r.Form.Get("token")+"|"+r.Form.Get("client_id")+"|"+r.Form.Get("token_type_hint"))
		is.mu.Unlock()
	})
	mux.HandleFunc("/models", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer at-") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		io.WriteString(w, `{"models":[{"slug":"gpt-a","display_name":"GPT A","visibility":"list","context_window":272000},{"slug":"gpt-hidden","display_name":"H","visibility":"hide"},{"slug":"","visibility":"list"}]}`)
	})
	return is
}

func (is *issuer) opts(dir string) Options {
	return Options{Path: filepath.Join(dir, ".sleipnir", "chatgpt.json"), Issuer: is.ts.URL, Models: is.ts.URL + "/models", AppName: "Sleipnir Test"}
}

func jwt(claims map[string]any) string {
	b, _ := json.Marshal(claims)
	return base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256"}`)) + "." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

func (is *issuer) token(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	fail := func(status int, code string) {
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(map[string]string{"error": code, "error_description": "the issuer says " + code})
	}
	is.mu.Lock()
	defer is.mu.Unlock()
	if r.Form.Get("resource") != Resource {
		fail(400, "invalid_target")
		return
	}
	switch r.Form.Get("grant_type") {
	case "authorization_code":
		sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		if base64.RawURLEncoding.EncodeToString(sum[:]) != is.challenge {
			fail(400, "invalid_grant") // the verifier does not answer the challenge
			return
		}
		if r.Form.Get("client_id") != is.clientID || r.Form.Get("code") != "the-code" {
			fail(400, "invalid_client")
			return
		}
		is.refresh = "rt-1"
		claims := map[string]any{"iss": is.ts.URL, "aud": is.clientID, "sub": "user-1", "email": "me@example.com", "name": "Me",
			"nonce": is.nonce, "exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix()}
		if is.idClaims != nil {
			is.idClaims(claims)
		}
		json.NewEncoder(w).Encode(map[string]any{"access_token": "at-1", "token_type": "Bearer", "expires_in": is.expiresIn,
			"refresh_token": is.refresh, "id_token": jwt(claims), "scope": scopes})
	case "refresh_token":
		atomic.AddInt32(&is.refreshes, 1)
		if is.failRefresh != "" {
			fail(400, is.failRefresh)
			return
		}
		if r.Form.Get("client_id") != is.clientID || r.Form.Get("refresh_token") != is.refresh {
			fail(400, "invalid_grant") // a refresh token that was already used
			return
		}
		n := atomic.LoadInt32(&is.refreshes) + 1
		is.refresh = fmt.Sprintf("rt-%d", n)
		json.NewEncoder(w).Encode(map[string]any{"access_token": fmt.Sprintf("at-%d", n), "token_type": "bearer", "expires_in": is.expiresIn, "refresh_token": is.refresh, "scope": scopes})
	default:
		fail(400, "unsupported_grant_type")
	}
}

// browser plays the person's browser: it is given the authorization address, checks what the address must carry, and answers as the
// issuer's page would, by calling the redirect address with a code and the client id that was issued.
func (is *issuer) browser(extra url.Values) func(string) error {
	return func(authURL string) error {
		u, err := url.Parse(authURL)
		if err != nil {
			return err
		}
		q := u.Query()
		is.mu.Lock()
		is.challenge, is.nonce, is.authQuery = q.Get("code_challenge"), q.Get("nonce"), q
		is.mu.Unlock()
		back, _ := url.Parse(q.Get("redirect_uri"))
		bq := url.Values{"code": {"the-code"}, "state": {q.Get("state")}, "client_id": {is.clientID}}
		for k, v := range extra {
			bq[k] = v
		}
		back.RawQuery = bq.Encode()
		go func() {
			resp, err := http.Get(back.String())
			if err == nil {
				resp.Body.Close()
			}
		}()
		return nil
	}
}

func login(t *testing.T, is *issuer, dir string, open func(string) error) (*Store, string, error) {
	t.Helper()
	var out bytes.Buffer
	st, err := Login(context.Background(), is.opts(dir), LoginIO{Out: &out, Open: open, Wait: 10 * time.Second})
	return st, out.String(), err
}

func TestLoginFollowsTheProtocolAndKeepsTheSignInPrivate(t *testing.T) {
	is := newIssuer(t)
	dir := t.TempDir()
	st, out, err := login(t, is, dir, is.browser(nil))
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	q := is.authQuery
	for k, want := range map[string]string{
		"client_id": dynamicClientID, "response_type": "code", "scope": scopes, "resource": Resource,
		"code_challenge_method": "S256", "agent_name_hint": "Sleipnir Test",
	} {
		if q.Get(k) != want {
			t.Errorf("authorization request %s = %q, want %q", k, q.Get(k), want)
		}
	}
	if !strings.HasPrefix(q.Get("redirect_uri"), "http://127.0.0.1:") || !strings.HasSuffix(q.Get("redirect_uri"), "/auth/callback") {
		t.Errorf("redirect_uri = %q: a port of this machine", q.Get("redirect_uri"))
	}
	if !strings.HasPrefix(q.Get("ext_agent_host_id"), "urn:uuid:") || q.Get("state") == "" || q.Get("nonce") == "" {
		t.Errorf("host id %q, state %q, nonce %q", q.Get("ext_agent_host_id"), q.Get("state"), q.Get("nonce"))
	}
	if !strings.Contains(out, is.ts.URL+"/authorize?") {
		t.Errorf("the person is given the address to open:\n%s", out)
	}
	if who := st.Who(); who != "me@example.com" {
		t.Errorf("who = %q", who)
	}
	fi, err := os.Stat(is.opts(dir).Path)
	if err != nil || (runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600) { // Windows keeps no POSIX modes
		t.Errorf("the file is %v, %v: for its owner only", fi, err)
	}
	c, _ := readConnection(is.opts(dir).Path)
	if c.ClientID != is.clientID || c.HostID != q.Get("ext_agent_host_id") || c.RefreshToken != "rt-1" || c.AccessToken != "at-1" {
		t.Errorf("the connection: %+v", c)
	}
	tok, err := st.Token(context.Background())
	if err != nil || tok != "at-1" {
		t.Errorf("the token is the one that was issued, not renewed: %q %v", tok, err)
	}
}

// A person who signs in again is the same installation and the same client: the host id and the issued client id are reused.
func TestSigningInAgainReusesTheRegistration(t *testing.T) {
	is := newIssuer(t)
	dir := t.TempDir()
	if _, out, err := login(t, is, dir, is.browser(nil)); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	first := is.authQuery.Get("ext_agent_host_id")
	if _, out, err := login(t, is, dir, is.browser(nil)); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if is.authQuery.Get("ext_agent_host_id") != first || is.authQuery.Get("client_id") != is.clientID || is.authQuery.Get("login_hint") != "me@example.com" {
		t.Errorf("second sign-in asked with %v", is.authQuery)
	}
}

func TestACallbackThatIsNotTheAnswerIsIgnoredAndOneThatIsRefusedEndsTheSignIn(t *testing.T) {
	is := newIssuer(t)
	// a page that makes the browser call the port with another state is not the answer: it is ignored, and the real one still works
	open := func(authURL string) error {
		u, _ := url.Parse(authURL)
		back, _ := url.Parse(u.Query().Get("redirect_uri"))
		back.RawQuery = url.Values{"code": {"evil"}, "state": {"not-the-state"}, "client_id": {"x"}}.Encode()
		resp, err := http.Get(back.String())
		if err != nil {
			return err
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Error("a wrong state was answered as a sign-in")
		}
		return is.browser(nil)(authURL)
	}
	if _, out, err := login(t, is, t.TempDir(), open); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	_, _, err := login(t, is, t.TempDir(), is.browser(url.Values{"error": {"access_denied"}, "code": {""}}))
	if err == nil || !strings.Contains(err.Error(), "access_denied") {
		t.Errorf("a refused sign-in says so: %v", err)
	}
}

func TestAnIDTokenThatIsNotForThisSignInIsRefused(t *testing.T) {
	for name, mutate := range map[string]func(map[string]any){
		"nonce":   func(c map[string]any) { c["nonce"] = "someone else's" },
		"issuer":  func(c map[string]any) { c["iss"] = "https://evil.example" },
		"client":  func(c map[string]any) { c["aud"] = "another_client" },
		"expired": func(c map[string]any) { c["exp"] = time.Now().Add(-time.Hour).Unix() },
		"subject": func(c map[string]any) { c["sub"] = "" },
	} {
		is := newIssuer(t)
		is.idClaims = mutate
		if _, _, err := login(t, is, t.TempDir(), is.browser(nil)); err == nil || !strings.Contains(err.Error(), "ID token") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestAnIssuerWhoseMetadataPointsElsewhereIsRefused(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"issuer": "http://" + r.Host, "authorization_endpoint": "http://" + r.Host + "/a", "token_endpoint": "https://evil.example/token"})
	}))
	defer bad.Close()
	_, err := Login(context.Background(), Options{Path: filepath.Join(t.TempDir(), "c.json"), Issuer: bad.URL}, LoginIO{Wait: time.Second})
	if err == nil || !strings.Contains(err.Error(), "not on its own host") {
		t.Errorf("%v", err)
	}
}

func TestAnExpiredTokenIsRenewedOnceAndTheRotatedTokenIsKept(t *testing.T) {
	is := newIssuer(t)
	is.expiresIn = 30 // inside the two minutes: due at once
	dir := t.TempDir()
	st, out, err := login(t, is, dir, is.browser(nil))
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	is.expiresIn = 3600
	var wg sync.WaitGroup
	toks := make([]string, 8)
	for i := range toks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			toks[i], _ = st.Token(context.Background())
		}()
	}
	wg.Wait()
	for _, tk := range toks {
		if tk != toks[0] || !strings.HasPrefix(tk, "at-") || tk == "at-1" {
			t.Fatalf("every caller gets the renewed token: %v", toks)
		}
	}
	if n := atomic.LoadInt32(&is.refreshes); n != 1 {
		t.Errorf("%d renewals for 8 callers: the refresh token rotates, so two at once would spoil it", n)
	}
	c, _ := readConnection(is.opts(dir).Path)
	if c.RefreshToken != is.refresh || c.AccessToken != toks[0] {
		t.Errorf("the rotated refresh token is kept: %+v (issuer: %s)", c, is.refresh)
	}
}

// Another sign-in of the same person (another sleipnir process) renewed first: its refresh token is the one on disk, and the one held
// here is refused. What is on disk is taken, not an error.
func TestARefreshTokenSpentByAnotherProcessIsReplacedByWhatIsOnDisk(t *testing.T) {
	is := newIssuer(t)
	is.expiresIn = 30
	dir := t.TempDir()
	st, out, err := login(t, is, dir, is.browser(nil))
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	other, err := Open(is.opts(dir)) // the other process
	if err != nil {
		t.Fatal(err)
	}
	is.expiresIn = 3600
	otherTok, err := other.Token(context.Background()) // it renews: rt-1 is spent
	if err != nil {
		t.Fatal(err)
	}
	tok, err := st.Token(context.Background()) // this one still holds rt-1
	if err != nil || tok != otherTok {
		t.Errorf("token %q, %v: should adopt what the other process kept (%q)", tok, err, otherTok)
	}
	if n := atomic.LoadInt32(&is.refreshes); n != 1 {
		t.Errorf("%d renewals: only the first needed the issuer", n)
	}
}

func TestAnEndedSignInSaysToSignInAgainAndATemporaryFailureDoesNot(t *testing.T) {
	is := newIssuer(t)
	is.expiresIn = 30
	st, out, err := login(t, is, t.TempDir(), is.browser(nil))
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	is.mu.Lock()
	is.failRefresh = "invalid_grant"
	is.mu.Unlock()
	_, err = st.Token(context.Background())
	var re *refreshError
	if err == nil || !strings.Contains(err.Error(), "sleipnir login chatgpt") || !asRefresh(err, &re) || re.temporary {
		t.Errorf("a refused renewal is final and says what to do: %v", err)
	}
	// a server that is down is temporary, and a token that has not expired yet still works meanwhile
	is2 := newIssuer(t)
	is2.expiresIn = 100 // within the margin, not yet expired
	st2, out, err := login(t, is2, t.TempDir(), is2.browser(nil))
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	is2.ts.Close()
	tok, err := st2.Token(context.Background())
	if err != nil || tok != "at-1" {
		t.Errorf("with the issuer out of reach the token that is still good is used: %q %v", tok, err)
	}
}

func asRefresh(err error, target **refreshError) bool {
	re, ok := err.(*refreshError)
	if ok {
		*target = re
	}
	return ok
}

func TestARefusedTokenIsRenewedNext(t *testing.T) {
	is := newIssuer(t)
	st, out, err := login(t, is, t.TempDir(), is.browser(nil))
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	st.Refused("at-1")
	tok, err := st.Token(context.Background())
	if err != nil || tok == "at-1" {
		t.Errorf("%q %v", tok, err)
	}
}

func TestLogoutRevokesAndForgetsTheTokensButKeepsTheRegistration(t *testing.T) {
	is := newIssuer(t)
	dir := t.TempDir()
	st, out, err := login(t, is, dir, is.browser(nil))
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if err := st.Logout(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(is.revoked) != 1 || is.revoked[0] != "rt-1|client_abc123|refresh_token" {
		t.Errorf("revoked %v", is.revoked)
	}
	if _, err := Open(is.opts(dir)); err != ErrNotConnected {
		t.Errorf("after a sign-out there is no sign-in: %v", err)
	}
	c, _ := readConnection(is.opts(dir).Path)
	if c.HostID == "" || c.ClientID != is.clientID || c.RefreshToken != "" || c.AccessToken != "" || c.IDToken != "" {
		t.Errorf("the registration stays, the tokens go: %+v", c)
	}
	if Connected(is.opts(dir).Path) {
		t.Error("Connected after a sign-out")
	}
}

func TestModelsAreTheOnesTheListOffers(t *testing.T) {
	is := newIssuer(t)
	st, out, err := login(t, is, t.TempDir(), is.browser(nil))
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	ms, err := st.Models(context.Background())
	if err != nil || len(ms) != 1 || ms[0].Slug != "gpt-a" || ms[0].DisplayName != "GPT A" || ms[0].ContextWindow != 272000 {
		t.Errorf("%+v %v", ms, err)
	}
}

func TestAPastedAddressCompletesASignInWhereNoBrowserCanReachThePort(t *testing.T) {
	is := newIssuer(t)
	pasted := make(chan string, 1)
	open := func(authURL string) error {
		u, _ := url.Parse(authURL)
		q := u.Query()
		is.mu.Lock()
		is.challenge, is.nonce, is.authQuery = q.Get("code_challenge"), q.Get("nonce"), q
		is.mu.Unlock()
		back, _ := url.Parse(q.Get("redirect_uri"))
		back.RawQuery = url.Values{"code": {"the-code"}, "state": {q.Get("state")}, "client_id": {is.clientID}}.Encode()
		pasted <- back.String()
		return fmt.Errorf("no browser here") // so the address is asked for
	}
	var out bytes.Buffer
	st, err := Login(context.Background(), is.opts(t.TempDir()), LoginIO{Out: &out, Open: open, Paste: func() (string, error) { return <-pasted, nil }, Wait: 10 * time.Second})
	if err != nil || st == nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
}

func TestNotSignedInIsAnError(t *testing.T) {
	if _, err := Open(Options{Path: filepath.Join(t.TempDir(), "none.json")}); err != ErrNotConnected {
		t.Errorf("%v", err)
	}
}
