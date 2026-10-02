package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/chatgptauth"
	"github.com/anemos-labs/sleipnir/internal/provider"
)

// A key the provider refused is entered again with the command of the place the person is: `sleipnir login` on a command line, /login in the
// chat. A ChatGPT sign-in that has ended already says to sign in again, and an error that is not a refusal has no hint.
func TestTheHintForARefusedKeyNamesTheWayToEnterItWhereThePersonIs(t *testing.T) {
	refused := &provider.Error{Kind: provider.ErrAuth, Status: 401, Message: "bad key"}
	if h := authHint(refused, "`sleipnir login`"); !strings.Contains(h, "with `sleipnir login`;") || strings.Contains(h, "/login") {
		t.Errorf("on a command line: %q", h)
	}
	if h := authHint(refused, "/login"); !strings.Contains(h, "with /login;") || strings.Contains(h, "sleipnir login") {
		t.Errorf("in the chat: %q", h)
	}
	ended := &provider.Error{Kind: provider.ErrAuth, Message: chatgptauth.ErrNotConnected.Error(), Err: chatgptauth.ErrNotConnected}
	if h := authHint(ended, "/login"); h != "" {
		t.Errorf("a sign-in that is gone says so itself: %q", h)
	}
	if h := authHint(errors.New("boom"), "/login"); h != "" {
		t.Errorf("not a refusal: %q", h)
	}
}
