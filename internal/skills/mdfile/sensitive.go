package mdfile

import (
	"path"
	"strings"
)

// SensitivePath returns why a project-relative slash path must not be included
// automatically, or "". It is a backstop for the obvious cases (credential
// files, repository internals) so that "@.env" in a template or an argument does
// not put secrets in a prompt even where the caller supplied no AllowRead; the
// permission engine's own policy is the real one.
func SensitivePath(rel string) string {
	segs := strings.Split(strings.ToLower(rel), "/")
	for _, s := range segs[:len(segs)-1] {
		switch s {
		case ".git", ".ssh", ".aws", ".gnupg", ".kube", ".docker":
			return "it is inside a credentials or repository-internal directory"
		}
	}
	base := segs[len(segs)-1]
	switch {
	case base == ".env" || strings.HasPrefix(base, ".env.") && !strings.HasSuffix(base, ".example") && !strings.HasSuffix(base, ".sample") && !strings.HasSuffix(base, ".template"):
		return "it is an environment file that may hold secrets"
	case base == ".git" || base == ".npmrc" || base == ".netrc" || base == ".pypirc" || base == ".git-credentials" || base == ".htpasswd":
		return "it is a credentials file"
	case strings.HasPrefix(base, "id_rsa") || strings.HasPrefix(base, "id_dsa") || strings.HasPrefix(base, "id_ecdsa") || strings.HasPrefix(base, "id_ed25519"):
		if strings.HasSuffix(base, ".pub") {
			return ""
		}
		return "it looks like a private key"
	}
	switch path.Ext(base) {
	case ".pem", ".key", ".p12", ".pfx", ".jks", ".keystore", ".kdbx", ".ppk":
		return "it looks like a private key or keystore"
	}
	return ""
}
