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
	if h := providerHint(refused, "`sleipnir login`"); !strings.Contains(h, "with `sleipnir login`;") || strings.Contains(h, "/login") {
		t.Errorf("on a command line: %q", h)
	}
	if h := providerHint(refused, "/login"); !strings.Contains(h, "with /login;") || strings.Contains(h, "sleipnir login") {
		t.Errorf("in the chat: %q", h)
	}
	ended := &provider.Error{Kind: provider.ErrAuth, Message: chatgptauth.ErrNotConnected.Error(), Err: chatgptauth.ErrNotConnected}
	if h := providerHint(ended, "/login"); h != "" {
		t.Errorf("a sign-in that is gone says so itself: %q", h)
	}
	if h := providerHint(errors.New("boom"), "/login"); h != "" {
		t.Errorf("not a refusal: %q", h)
	}
}

// An unknown model and an account without credit say what to do about it, where the person is (a command line or the chat).
func TestTheHintForAnUnknownModelAndAnEmptyAccount(t *testing.T) {
	unknown := &provider.Error{Kind: provider.ErrBadRequest, Status: 404, Message: "The model `x` does not exist"}
	if h := providerHint(unknown, "`sleipnir login`"); !strings.Contains(h, "does not know this model") || !strings.Contains(h, "`sleipnir models`") {
		t.Errorf("on a command line: %q", h)
	}
	if h := providerHint(unknown, "/login"); !strings.Contains(h, "/model") || strings.Contains(h, "sleipnir models") {
		t.Errorf("in the chat: %q", h)
	}
	if h := providerHint(&provider.Error{Kind: provider.ErrPayment, Status: 402, Message: "no credit"}, "/login"); !strings.Contains(h, "no credit left") {
		t.Errorf("an empty account: %q", h)
	}
	if h := providerHint(&provider.Error{Kind: provider.ErrBadRequest, Status: 400, Message: "bad"}, "/login"); h != "" {
		t.Errorf("a bad request that is not a missing model has no hint: %q", h)
	}
}
