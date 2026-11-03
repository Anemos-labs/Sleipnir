package provider

import (
	"net/url"
	"testing"
)

// Origins compare their hosts by ASCII case only: a name that Unicode lowercasing would make equal to another, but that IDNA sends
// elsewhere, is another origin.
func TestSameOriginFoldsOnlyASCIICase(t *testing.T) {
	u := func(s string) *url.URL { v, _ := url.Parse(s); return v }
	if !SameOrigin(u("https://API.Example.com/x"), u("https://api.example.com./y")) {
		t.Error("ASCII case and a trailing dot made two origins")
	}
	if SameOrigin(u("https://İ.example.com/"), u("https://i̇.example.com/")) {
		t.Error("two names IDNA keeps apart were taken for one origin")
	}
}
