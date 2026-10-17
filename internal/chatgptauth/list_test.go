package chatgptauth

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

// A list asked with a deadline must not renew the sign-in: the refresh token rotates, and a renewal cut off after the issuer answered
// would lose it. With a token that is due, ModelsNoRenewal asks nothing at all; Models renews first; once the token is fresh, both ask.
func TestTheListIsAskedWithoutARenewalOnlyWithATokenThatNeedsNone(t *testing.T) {
	is := newIssuer(t)
	is.expiresIn = 30 // inside the two minutes: due at once
	st, out, err := login(t, is, t.TempDir(), is.browser(nil))
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	is.expiresIn = 3600
	if ms, err := st.ModelsNoRenewal(context.Background()); !errors.Is(err, ErrRenewalDue) || ms != nil {
		t.Fatalf("a token that is due: %+v %v", ms, err)
	}
	if n, l := atomic.LoadInt32(&is.refreshes), is.listed.Load(); n != 0 || l != 0 {
		t.Fatalf("%d renewals and %d list requests: nothing may be asked", n, l)
	}
	if ms, err := st.Models(context.Background()); err != nil || len(ms) != 1 || atomic.LoadInt32(&is.refreshes) != 1 {
		t.Fatalf("Models renews first: %+v %v", ms, err)
	}
	ms, err := st.ModelsNoRenewal(context.Background())
	if err != nil || len(ms) != 1 || ms[0].Slug != "gpt-a" || ms[0].ContextWindow != 272000 || is.listed.Load() != 2 {
		t.Errorf("with a fresh token: %+v %v (%d list requests)", ms, err, is.listed.Load())
	}
	if n := atomic.LoadInt32(&is.refreshes); n != 1 {
		t.Errorf("%d renewals", n)
	}
}

// A kept list holds slugs, names and windows, never a token; it is the owner's only; and it is checked as an answer from the API is
// when it is read back, so an edited file cannot bring in an entry the API's answer could not.
func TestAKeptListHoldsNoTokenAndIsCheckedLikeAnAnswer(t *testing.T) {
	is := newIssuer(t)
	home := t.TempDir()
	st, out, err := login(t, is, home, is.browser(nil))
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	ms, err := st.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "cache") // not there yet
	path := st.ListPath(dir)
	if filepath.Dir(path) != dir || !strings.HasPrefix(filepath.Base(path), "chatgpt-models-") {
		t.Fatalf("path %s", path)
	}
	if err := SaveList(path, ms); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	conn, _ := readConnection(is.opts(home).Path)
	for _, secret := range []string{conn.AccessToken, conn.RefreshToken, conn.IDToken, conn.ClientID, conn.HostID} {
		if secret != "" && strings.Contains(string(b), secret) {
			t.Fatalf("the kept list holds a secret of the sign-in: %s", b)
		}
	}
	if fi, _ := os.Stat(path); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", fi.Mode().Perm())
	}
	got, at, err := LoadList(path)
	if err != nil || len(got) != 1 || got[0] != ms[0] || at.IsZero() {
		t.Fatalf("read back: %+v %v %v", got, at, err)
	}

	edited := `{"version":1,"models":[{"slug":"gpt-a","context_window":-5},{"slug":""},{"slug":"` + strings.Repeat("x", 201) +
		`"},{"slug":"gpt-b","display_name":"` + strings.Repeat("n", 300) + `","context_window":400000}]}`
	if err := os.WriteFile(path, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	got, _, err = LoadList(path)
	if err != nil || len(got) != 2 || got[0] != (Model{Slug: "gpt-a"}) || got[1].Slug != "gpt-b" || got[1].ContextWindow != 400000 || len(got[1].DisplayName) > 203 {
		t.Errorf("an edited list: %+v %v", got, err)
	}
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, _, err := LoadList(path); err == nil || got != nil {
		t.Errorf("a file that is not a list: %+v %v", got, err)
	}
}

// The list differs between accounts and between endpoints, and so does where it is kept.
func TestAListIsKeptPerAccountAndEndpoint(t *testing.T) {
	store := func(models, subject, email string) *Store {
		return &Store{opts: Options{Models: models}, c: connection{Subject: subject, Email: email}}
	}
	dir := t.TempDir()
	a := store("https://api.example/v1/models", "user-1", "a@example.com").ListPath(dir)
	if b := store("https://api.example/v1/models", "user-1", "a@example.com").ListPath(dir); a != b {
		t.Errorf("the same account and endpoint: %s and %s", a, b)
	}
	for _, other := range []*Store{
		store("https://api.example/v1/models", "user-2", "a@example.com"),
		store("https://proxy.example/v1/models", "user-1", "a@example.com"),
		store("https://api.example/v1/models", "", "a@example.com"),
	} {
		if p := other.ListPath(dir); p == a {
			t.Errorf("%+v %+v is kept where user-1's list is", other.opts, other.c)
		}
	}
}
