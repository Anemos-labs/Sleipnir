package weburl

import (
	"net/url"
	"strings"
	"testing"
	"time"
)

// A scoped IPv6 literal whose zone spells an allowed domain is no DNS name: a domain rule must not match it.
func TestAScopedAddressIsNotADomainName(t *testing.T) {
	for _, raw := range []string{"http://[fe80::1%25.example.com]/", "http://[2606:4700::1%25evil.example.com]:8080/x"} {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		if h, ok := HostChecked(u); ok {
			t.Errorf("%s: %q passes as a name", raw, h)
		}
	}
	for _, raw := range []string{"http://[::1]/", "http://127.0.0.1/", "https://_dmarc.example.com/", "https://xn--bcher-kva.test/"} {
		u, _ := url.Parse(raw)
		if _, ok := HostChecked(u); !ok {
			t.Errorf("%s is refused", raw)
		}
	}
}

// A long label of many distinct characters is refused before it is encoded, quickly.
func TestALongInternationalLabelIsRefusedQuickly(t *testing.T) {
	var b strings.Builder
	for r := rune(0x4e00); b.Len() < 60000; r++ {
		b.WriteRune(r)
	}
	start := time.Now()
	if _, ok := ASCIIName(b.String() + ".test"); ok {
		t.Fatal("a label far past 63 characters was encoded")
	}
	if _, ok := ASCIIName(strings.Repeat("ü", 64) + ".test"); ok {
		t.Fatal("a 64-character label was encoded")
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("took %v", d)
	}
	if a, ok := ASCIIName("bücher.test"); !ok || a != "xn--bcher-kva.test" {
		t.Fatalf("%q %v", a, ok)
	}
}
