package chatgptauth

import (
	"bytes"
	"context"
	"os"
	"testing"
	"time"
)

func TestADeclinedAccountIsNotKept(t *testing.T) {
	is := newIssuer(t)
	o := is.opts(t.TempDir())
	var out bytes.Buffer
	asked := ""
	_, err := Login(context.Background(), o, LoginIO{Out: &out, Open: is.browser(nil), Wait: 10 * time.Second,
		Confirm: func(email, _ string) (bool, error) { asked = email; return false, nil }})
	if err == nil {
		t.Fatal("a declined account was accepted")
	}
	if asked == "" {
		t.Fatal("the account was not shown before it was kept")
	}
	if _, err := os.Stat(o.Path); !os.IsNotExist(err) {
		t.Fatalf("a declined sign-in left a connection behind: %v", err)
	}
}
