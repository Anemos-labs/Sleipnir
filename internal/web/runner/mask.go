package runner

import (
	"crypto/rand"
	"encoding/hex"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/harden"
	"github.com/anemos-labs/sleipnir/internal/rl/redact"
	"github.com/anemos-labs/sleipnir/internal/tools"
	"github.com/anemos-labs/sleipnir/internal/web"
)

// masker makes a line of a command's output safe to show in the page: terminal control sequences go (tools.SanitizeForTerminal),
// the provider keys the server holds and its run token are replaced by [redacted] wherever they appear, and credential-shaped
// strings (provider tokens, JWTs, private keys, passwords in URLs, bearer values) by redaction tokens. A command's output is data:
// what a command prints about a key is never the key in the page.
type masker struct {
	srv *web.Server
	red *redact.Redactor
}

// newMasker makes the masker of a server's runs (srv may be nil in tests).
func newMasker(srv *web.Server) *masker {
	var salt [8]byte
	_, _ = rand.Read(salt[:])
	return &masker{srv: srv, red: redact.New(redact.Config{
		Salt:  hex.EncodeToString(salt[:]),
		Kinds: []string{redact.GroupTokens, redact.KindJWT, redact.KindPrivateKey, redact.KindURLCred, redact.KindBearer},
	})}
}

// Clean is the masker of the runner's output, for other route packages that show text a command or a file wrote (a job's log).
func Clean(text string) string { return defaultMasker.clean(text) }

// defaultMasker masks without a server's token (the token never reaches a child's output: it is not in its environment or its
// arguments).
var defaultMasker = newMasker(nil)

// clean returns the line as the page may show it.
func (m *masker) clean(s string) string {
	s = tools.SanitizeForTerminal(s)
	for _, name := range harden.Held() {
		if v, ok := harden.LookupSecret(name); ok && len(v) >= 8 {
			s = strings.ReplaceAll(s, v, "[redacted]")
		}
	}
	if m.srv != nil {
		if t := m.srv.Token(); len(t) >= 8 {
			s = strings.ReplaceAll(s, t, "[redacted]")
		}
	}
	return m.red.String(s)
}
