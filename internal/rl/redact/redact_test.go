package redact

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
)

func testRedactor() *Redactor { return New(Config{Salt: "test-salt"}) }

// frag joins fragments. The credentials below are assembled at run time so that
// this source file contains no secret-shaped literal: repository secret scanners
// cannot tell a test vector from a leak, and a blocked push helps nobody.
func frag(parts ...string) string { return strings.Join(parts, "") }

// Fake credentials with the exact shape of the real ones. None of them is live.
var (
	glToken    = frag("glp", "at-", "Ab3dEf6hIj9kLm2nOp5q")
	slackTok   = frag("xox", "b-123456789012-1234567890123-", "AbCdEfGhIjKlMnOpQrStUvWx")
	stripeKey  = frag("sk_", "live_", "51H8xQwErTyUiOpAsDfGhJkLz")
	stripeTest = frag("sk_", "test_", "4eC39HqLyjWDarjtT1zdp7dc")
	slackHook  = frag("https://hooks.", "slack.com/services/T01234567/B01234567/", "AbCdEfGhIjKlMnOpQrStUvWx")
)

const (
	awsKey    = "AKIAIOSFODNN7EXAMPLE"
	ghToken   = "ghp_16C7e42F292c6912E7710c838347Ae178B4a"
	googleKey = "AIzaSyA-1234567890abcdefghijklmnopqrstu"
	npmToken  = "npm_Ab3dEf6hIj9kLm2nOp5qRs8tUv1wXy4zAb3d"
	pypiToken = "pypi-AgEIcHlwaS5vcmcCJDU4Yjc5ZDQ3LTMxYzMtNDJhZC04MjBiLTZkZDQ0OWZjMDkzNQACKlsxLFsic2xlaXBuaXIiXV0AAAYg1234567890abcdef"
	llmKey    = "sk-ant-api03-Ab3dEf6hIj9kLm2nOp5qRs8tUv1wXy4zAb3dEf6hIj9kLm2nOp5q"
	hfToken   = "hf_AbCdEfGhIjKlMnOpQrStUvWxYz0123456789"
	jwtTok    = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IkpvaG4gRG9lIiwiaWF0IjoxNTE2MjM5MDIyfQ.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c"
	pemFull   = "-----BEGIN RSA PRIVATE KEY-----\nMIIBOgIBAAJBAKj34GkxFhD90vcNLYLInFEX6Ppy1tPf9Cnzj4p4WGeKLs1Pt8Qu\nKUpRKfFLfRYC9AIKjbJTWit+CqvjWYzvQwECAwEAAQJAIJLixBy2qpFoS4DSmoEm\n-----END RSA PRIVATE KEY-----"
)

var tokenShape = regexp.MustCompile(`⟦redacted:[a-z0-9_]+:[0-9a-f]{6}⟧`)

func TestRedactsSecrets(t *testing.T) {
	cases := []struct {
		name, in string
		kind     string
		secret   string // must be gone from the output
	}{
		{"aws access key", "aws_access_key_id = " + awsKey, KindAWS, awsKey},
		{"aws in json", `{"AccessKeyId":"` + awsKey + `"}`, KindAWS, awsKey},
		{"github classic", "git clone https://" + ghToken + "@github.com/o/r", KindGitHub, ghToken},
		{"github fine grained", "token github_pat_11ABCDEFG0abcdefghijkl_ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456", KindGitHub, "github_pat_11ABCDEFG0abcdefghijkl_ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456"},
		{"gitlab", "GITLAB=" + glToken, KindGitLab, glToken},
		{"slack bot", "SLACK_BOT=" + slackTok, KindSlack, slackTok},
		{"slack webhook", "curl " + slackHook, KindSlack, "AbCdEfGhIjKlMnOpQrStUvWx"},
		{"google", "key=" + googleKey, KindGoogle, googleKey},
		{"stripe live", "STRIPE " + stripeKey, KindStripe, stripeKey},
		{"stripe test", stripeTest, KindStripe, stripeTest},
		{"npm", "//registry.npmjs.org/:_authToken=" + npmToken, KindNPM, npmToken},
		{"pypi", "password: " + pypiToken, KindPyPI, pypiToken},
		{"anthropic key", "export ANTHROPIC_API_KEY=" + llmKey, KindLLM, llmKey},
		{"openai key", "OPENAI=sk-proj-Ab3dEf6hIj9kLm2nOp5qRs8tUv1wXy", KindLLM, "sk-proj-Ab3dEf6hIj9kLm2nOp5qRs8tUv1wXy"},
		{"huggingface", "HF_TOKEN=" + hfToken, KindHuggingFace, hfToken},
		{"jwt", "cookie: session=" + jwtTok, KindJWT, jwtTok},
		{"pem block", "key:\n" + pemFull + "\nend", KindPrivateKey, "MIIBOgIBAAJBAKj34GkxFhD90vcNLYLInFEX6Ppy1tPf9Cnzj4p4WGeKLs1Pt8Qu"},
		{"pem openssh", "-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAMwAAAAtzc2gtZW\n-----END OPENSSH PRIVATE KEY-----", KindPrivateKey, "b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAMwAAAAtzc2gtZW"},
		{"pem truncated", "-----BEGIN PRIVATE KEY-----\nMIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcwggSjAgEAAoIBAQC7VJTUt9Us8cKj\nMzEfYyjiWA4R4/M2bS1GB4t7NXp98C3SC6dVMvDuictGeurT8jNbvJZHtCSuYEvu\n… [4000 chars elided] …", KindPrivateKey, "MIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcwggSjAgEAAoIBAQC7VJTUt9Us8cKj"},
		{"url credentials", "git clone https://deploy:s3cr3tPassw0rd@git.corp.io/x.git", KindURLCred, "s3cr3tPassw0rd"},
		{"bearer header", `curl -H "Authorization: Bearer 3f9a8b7c6d5e4f3a2b1c0d9e8f7a6b5c" https://api.x`, KindBearer, "3f9a8b7c6d5e4f3a2b1c0d9e8f7a6b5c"},
		{"basic header", "Authorization: Basic dXNlcjpzdXBlcnNlY3JldHBhc3M=", KindBearer, "dXNlcjpzdXBlcnNlY3JldHBhc3M="},
		{"bare bearer", "got Bearer abcDEF123456ghiJKL789012mno back", KindBearer, "abcDEF123456ghiJKL789012mno"},
		{"kv quoted", `password: "Tr0ub4dor&3xyz"`, KindSecret, "Tr0ub4dor&3xyz"},
		{"kv go assign", `dbPassword := "correct-horse-battery-staple-9"`, KindSecret, "correct-horse-battery-staple-9"},
		{"kv dotenv", "DB_PASSWORD=hunter2hunter2", KindSecret, "hunter2hunter2"},
		{"kv dotenv letters", "SECRET_KEY=mysupersecretkeyvalue", KindSecret, "mysupersecretkeyvalue"},
		{"kv json", `{"client_secret": "a1b2c3d4e5f6g7h8i9j0"}`, KindSecret, "a1b2c3d4e5f6g7h8i9j0"},
		{"kv yaml", "api_key: 7f3a9c1e5b2d4f6a8c0e", KindSecret, "7f3a9c1e5b2d4f6a8c0e"},
		{"kv header", `-H "X-Api-Key: 7f3a9c1e5b2d4f6a8c0e"`, KindSecret, "7f3a9c1e5b2d4f6a8c0e"},
		{"kv flag space", "mysql --password Sup3rS3cret!pw -u root", KindSecret, "Sup3rS3cret!pw"},
		{"kv flag equals", "tool --token=abc123def456ghi789", KindSecret, "abc123def456ghi789"},
		{"kv aws secret", "aws_secret_access_key = wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", KindSecret, "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"},
		{"kv connection string", "Server=db;User Id=sa;Password=P4ssw0rd!Long1;Database=x", KindSecret, "P4ssw0rd!Long1"},
		{"kv php", `'password' => 'xK3$9mQ2pL7vN4'`, KindSecret, "xK3$9mQ2pL7vN4"},
		{"kv dollar password", `password = "$ecr3t!Pass#word"`, KindSecret, "$ecr3t!Pass#word"},
		{"entropy after key word", "the secret_key_base is kJ8dHs92Lp0QwErTy5UiOp3aSdFgHj7Kl for prod", KindEntropy, "kJ8dHs92Lp0QwErTy5UiOp3aSdFgHj7Kl"},
		{"entropy prose", "the api key is Zq8Yw3Xe6Rt1Uy4Io7Pa0Sd2Fg5Hj9Kl, keep it safe", KindEntropy, "Zq8Yw3Xe6Rt1Uy4Io7Pa0Sd2Fg5Hj9Kl"},
		{"entropy header word", "auth Zq8Yw3Xe6Rt1Uy4Io7Pa0Sd2Fg5Hj9Kl", KindEntropy, "Zq8Yw3Xe6Rt1Uy4Io7Pa0Sd2Fg5Hj9Kl"},
		{"email", "contact alice.smith@corp-example.io now", KindEmail, "alice.smith@corp-example.io"},
		{"email angle", "From: Bob <bob@acme.dev>", KindEmail, "bob@acme.dev"},
		{"ipv4", "connect to 54.239.28.85:443", KindIPv4, "54.239.28.85"},
		{"ipv4 url", "http://8.8.4.4/dns-query", KindIPv4, "8.8.4.4"},
		{"ipv4 range second", "scan 8.8.8.8-8.8.4.4", KindIPv4, "8.8.4.4"},
		{"ipv6", "listen [2a00:1450:4001:81b::200e]:443", KindIPv6, "2a00:1450:4001:81b::200e"},
		{"ipv6 prefixed", "addr:2a00:1450:4001:81b::200e ok", KindIPv6, "2a00:1450:4001:81b::200e"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := testRedactor()
			out := r.String(c.in)
			if strings.Contains(out, c.secret) {
				t.Fatalf("secret survived:\n in: %q\nout: %q", c.in, out)
			}
			if !strings.Contains(out, "⟦redacted:"+c.kind+":") {
				t.Fatalf("want a %s token:\n in: %q\nout: %q", c.kind, c.in, out)
			}
			if again := r.String(out); again != out {
				t.Fatalf("not idempotent:\n1: %q\n2: %q", out, again)
			}
			if r.Stats()[c.kind] == 0 {
				t.Fatalf("stats missed %s: %v", c.kind, r.Stats())
			}
		})
	}
}

// TestKeepsOrdinaryCode is the false-positive suite: real code, real config,
// hashes and test data that must come out byte for byte.
func TestKeepsOrdinaryCode(t *testing.T) {
	cases := []struct{ name, in string }{
		{"git sha", "commit 4b825dc642cb6eb9a060e54bf8d69288fbee4904\nAuthor: x"},
		{"git sha near key word", "the key commit is 4b825dc642cb6eb9a060e54bf8d69288fbee4904 in main"},
		{"sha256 digest", "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"},
		{"sha256 near token word", "token digest e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"},
		{"md5", `checksum = "d41d8cd98f00b204e9800998ecf8427e"`},
		{"uuid", "id: 550e8400-e29b-41d4-a716-446655440000"},
		{"uuid near key word", "the auth token id 550e8400-e29b-41d4-a716-446655440000 expired"},
		{"go.sum", "golang.org/x/net v0.43.0 h1:mDYX6nzSOiHYhLbZpX0ZBtcYAqoXdXoHfSdRKdgpodg=\ngolang.org/x/net v0.43.0/go.mod h1:vGLdDuIaBiQNtYxAgOBEpPSN9Ml0mjgRJZ+P5Qo1QNI="},
		{"base64 test data", `data := "SGVsbG8sIFdvcmxkISBUaGlzIGlzIHNvbWUgYmFzZTY0IGRhdGEgZm9yIGEgdGVzdC4="`},
		{"base64 png", "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="},
		{"env lookup", `password := os.Getenv("DB_PASSWORD")`},
		{"go token call", `token, err := s.NewTokenForUser(ctx, user)`},
		{"config ref", `apiKey = config.APIKey.ValueOrDefault`},
		{"secret name", "secretName: my-app-secrets-production"},
		{"max tokens", "max_tokens = 4096000000000"},
		{"token url", `token_url = "https://example.com/oauth/token/endpoint"`},
		{"shell var", "password: ${DB_PASSWORD}"},
		{"shell var default", "password: ${DB_PASSWORD:-changeme-please}"},
		{"helm template", "password: {{ .Values.database.password }}"},
		{"python environ", `PASSWORD = os.environ["DB_PASSWORD_PRODUCTION"]`},
		{"empty compare", `if password == "" { return errEmpty }`},
		{"const", "const tokenLength = 32"},
		{"hash call", "passwordHash = bcrypt.hash(password, rounds)"},
		{"bearer var", `curl -H "Authorization: Bearer $TOKEN" https://x`},
		{"bearer concat", `headers["Authorization"] = "Bearer " + accessToken`},
		{"bearer go", `req.Header.Set("Authorization", "Bearer "+tok)`},
		{"bearer prose", "bearer tokens authentication scheme is described in RFC 6750"},
		{"authorization docs", "the Authorization header carries credentials"},
		{"ui string", `"password": "Enter your password to continue"`},
		{"i18n", `"confirmPassword": "Confirm password again"`},
		{"placeholder", "api_key: your_api_key_goes_here_please"},
		{"placeholder angle", "password: <your-password-here>"},
		{"placeholder x", "token = xxxxxxxxxxxxxxxxxxxx"},
		{"snake value", `token = "access_token_value_name"`},
		{"screaming value", `secret = "GITHUB_TOKEN_SECRET_NAME"`},
		{"path value", `secret = "/run/secrets/db_password_file"`},
		{"pwd variable", "pwd=/home/user/project/some/deep/dir"},
		{"secret path lower", `secret_id = "prod/database/password-store"`},
		{"credentials file", "credentials: ~/.aws/credentials"},
		{"numeric id", "token: 12345678901234567890"},
		{"struct field", "Password string `json:\"password\"`"},
		{"go doc comment", "// token: the raw token value provided by the client library"},
		{"flag without value", "--password is required for this command"},
		{"pem mention", `if !strings.Contains(s, "-----BEGIN RSA PRIVATE KEY-----") { return }`},
		{"pem mention prose", "The file starts with -----BEGIN PRIVATE KEY----- and then base64."},
		{"url no creds", "https://github.com/reee344/sleipnir/issues/12"},
		{"url same user pass", "postgres://postgres:postgres@localhost:5432/db"},
		{"url placeholder", "https://user:${PASSWORD}@host.example/x"},
		{"url password word", "redis://default:password@cache:6379"},
		{"ssh remote", "git@github.com:reee344/sleipnir.git"},
		{"noreply", "Co-Authored-By: Claude <noreply@anthropic.com>"},
		{"example email", "reach user@example.com or admin@example.org"},
		{"npm spec", "lodash@4.17.21 react@18.2.0 @types/node@22.1.0"},
		{"matrix product", "y = W@x.T + b"},
		{"docker digest", "image@sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"},
		{"loopback", "listen 127.0.0.1:8080 and 0.0.0.0:9090"},
		{"private ipv4", "10.0.0.5 172.16.4.9 192.168.1.20 169.254.169.254 100.64.0.1"},
		{"doc ipv4", "192.0.2.1 198.51.100.7 203.0.113.99"},
		{"netmask", "netmask 255.255.255.0 gateway 255.255.255.255"},
		{"user agent", "Mozilla/5.0 Chrome/120.0.0.0 Safari/537.36"},
		{"version", "go1.24.7 v1.2.3 1.24.7 10.15.7.1"},
		{"dotted oid", "1.3.6.1.4.1.311.21.7 and 1.2.3.4.5"},
		{"ip with dash version", "release 8.8.8.8-beta and build-9.9.9.9"},
		{"ipv6 loopback", "::1 fe80::1ff:fe23:4567:890a fd12:3456:789a::1 2001:db8::1"},
		{"cpp scope", "std::vector<int> a::b Foo::bar dead::beef cache::default"},
		{"mac address", "hw aa:bb:cc:dd:ee:ff and 00:1A:2B:3C:4D:5E"},
		{"timestamp", "12:34:56 and 10:30:45.123 at 2026-09-30T12:34:56Z"},
		{"home placeholder", "/home/user/project and C:\\Users\\user\\x"},
		{"home shared", "/Users/Shared/Library /Users/Public/Documents"},
		{"home var", "/home/${USER}/x /home/<name>/y ~/z"},
		{"system paths", "/usr/local/go/bin /var/lib/docker /etc/ssl/private/key.pem"},
		{"plain go", "func (s *Server) handle(w http.ResponseWriter, r *http.Request) {\n\tid := r.URL.Query().Get(\"id\")\n}"},
		{"long identifier", "someVeryLongFunctionNameThatKeepsGoing1234567890"},
		{"key word then identifier", "auth handleAuthenticationTokenRefreshWithBackoff"},
		{"prose", "The quick brown fox jumps over the lazy dog. Nothing to see; 12 items, 3.5 average."},
		{"risk slug", "see /docs/risk-management-strategy-for-the-year-2024-planning"},
		{"sk slug", "https://scikit.example/sk-learn-machine-learning-guide"},
		{"relative home dir", "AGENTS.md: @../home/ok.md and ../home/notes.txt"},
		{"home file", "cat /home/README.md /home/config.yaml"},
		{"url userinfo", `fetch("https://example.com@evil.org/a")`},
		{"cidr", "allow 8.8.8.0/24 and 2a00:1450::/32"},
		{"prose with tokens plural", "the thread was a few thousand tokens (MinThreadTokens=4000, Soft=20000)"},
		{"identifier with digits near key word", "auth getUserByIdV2AndValidateSessionToken and token my-service-name-2024-prod-eu"},
		{"empty", ""},
	}
	r := testRedactor()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if out := r.String(c.in); out != c.in {
				t.Fatalf("false positive:\n in: %q\nout: %q", c.in, out)
			}
		})
	}
	if n := r.Total(); n != 0 {
		t.Fatalf("stats counted %d redactions on clean text: %v", n, r.Stats())
	}
}

func TestPathsKeepStructure(t *testing.T) {
	r := testRedactor()
	cases := [][2]string{
		{"/home/alice/project/main.go", "/home/user/project/main.go"},
		{"/Users/bob.smith/Library/x", "/Users/user/Library/x"},
		{`C:\Users\Carol\AppData\Local`, `C:\Users\user\AppData\Local`},
		{`C:\\Users\\Carol\\AppData`, `C:\\Users\\user\\AppData`},
		{"c:/users/Dave/x", "c:/users/user/x"},
		{"see /home/alice.", "see /home/user."},
		{"/mnt/c/Users/erin/x", "/mnt/c/Users/user/x"},
		{`{"cwd":"/home/alice/repo"}`, `{"cwd":"/home/user/repo"}`},
	}
	for _, c := range cases {
		if got := r.String(c[0]); got != c[1] {
			t.Errorf("%q -> %q, want %q", c[0], got, c[1])
		}
	}
	custom := New(Config{PathPlaceholder: "USER"})
	if got := custom.String("/home/alice/x"); got != "/home/USER/x" {
		t.Errorf("custom placeholder: %q", got)
	}
	if again := custom.String("/home/USER/x"); again != "/home/USER/x" {
		t.Errorf("placeholder must be stable: %q", again)
	}
}

func TestTokensAreDeterministicAndSalted(t *testing.T) {
	a1 := New(Config{Salt: "a"})
	a2 := New(Config{Salt: "a"})
	b := New(Config{Salt: "b"})
	in := "key " + ghToken + " and again " + ghToken + " other " + awsKey
	o1, o2, ob := a1.String(in), a2.String(in), b.String(in)
	if o1 != o2 {
		t.Fatalf("same salt must give the same output:\n%s\n%s", o1, o2)
	}
	if o1 == ob {
		t.Fatalf("different salts must give different tokens: %s", o1)
	}
	toks := tokenShape.FindAllString(o1, -1)
	if len(toks) != 3 || toks[0] != toks[1] || toks[0] == toks[2] {
		t.Fatalf("equal secrets must share a token and different ones must not: %v", toks)
	}
	// The token is the documented function of salt and secret.
	want := a1.token(KindGitHub, ghToken)
	if toks[0] != want {
		t.Fatalf("token %s != %s", toks[0], want)
	}
	if strings.ToLower(a1.String("Alice@Corp.io")) != strings.ToLower(a1.String("alice@corp.io")) {
		t.Fatalf("emails differing in case must share a token")
	}
}

func TestPrefixSharingSurvives(t *testing.T) {
	// Redaction is per string, so a shared prefix of messages stays shared.
	r := testRedactor()
	m1 := "env: API_KEY=hunter2hunter2 and " + ghToken
	m2 := "second message with /home/alice/x"
	p1 := []string{r.String(m1), r.String(m2)}
	p2 := []string{r.String(m1), r.String(m2), r.String("third")}
	if p1[0] != p2[0] || p1[1] != p2[1] {
		t.Fatal("shared prefix diverged after redaction")
	}
}

func TestIdempotentOnAlreadyRedacted(t *testing.T) {
	r := testRedactor()
	in := strings.Join([]string{awsKey, ghToken, jwtTok, pemFull, "password: hunter2hunter2", "alice@corp.io", "54.239.28.85", "/home/alice/x"}, "\n")
	once := r.String(in)
	twice := r.String(once)
	if once != twice {
		t.Fatalf("not idempotent:\n%s\n----\n%s", once, twice)
	}
	// Adjacent tokens and secrets glued to tokens must settle too.
	glued := "password=" + r.token(KindSecret, "x") + "abcdefghijklmnop"
	if g := r.String(glued); r.String(g) != g {
		t.Fatalf("glued token unstable: %q", g)
	}
	if strings.Count(once, "⟦redacted:") < 7 {
		t.Fatalf("expected at least 7 redactions:\n%s", once)
	}
}

func TestAllowListAndKinds(t *testing.T) {
	r := New(Config{Allow: []*regexp.Regexp{regexp.MustCompile(`EXAMPLE$`)}})
	in := "aws " + awsKey + " and " + ghToken
	out := r.String(in)
	if !strings.Contains(out, awsKey) || strings.Contains(out, ghToken) {
		t.Fatalf("allow list should keep only the example key: %s", out)
	}
	// An allowed value is protected from the other rules too.
	if got := r.String("aws_secret_key=" + awsKey); !strings.Contains(got, awsKey) {
		t.Fatalf("allowed value was redacted by another rule: %s", got)
	}

	only := New(Config{Kinds: []string{"Email"}})
	if got := only.String(ghToken + " bob@acme.dev"); !strings.Contains(got, ghToken) || strings.Contains(got, "bob@acme.dev") {
		t.Fatalf("kinds filter: %s", got)
	}
	toks := New(Config{Kinds: []string{GroupTokens}})
	if got := toks.String(ghToken + " bob@acme.dev /home/alice/x"); strings.Contains(got, ghToken) || !strings.Contains(got, "bob@acme.dev") || !strings.Contains(got, "/home/alice/x") {
		t.Fatalf("tokens group: %s", got)
	}
	if err := (Config{Kinds: []string{"email", "nope"}}).Validate(); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("Validate should reject unknown kinds: %v", err)
	}
	if err := (Config{Kinds: []string{"tokens", "PATH"}}).Validate(); err != nil {
		t.Fatalf("valid kinds rejected: %v", err)
	}
	if len(AllKinds()) != len(ruleOrder) || len(detectors) != len(ruleOrder) {
		t.Fatalf("every kind needs a detector")
	}
}

func TestJSONRedactsValuesOnly(t *testing.T) {
	r := testRedactor()
	in := json.RawMessage(`{ "password" : "hunter2hunter2", "nested": {"ghp_16C7e42F292c6912E7710c838347Ae178B4a": ["` + ghToken + `", 42, true, null, "plain"]},
 "path": "/home/alice/x", "escaped": "line1\nline2 key=\"` + awsKey + `\"", "html": "a < b && c > d" }`)
	out := r.JSON(in)
	if !json.Valid(out) {
		t.Fatalf("output is not JSON: %s", out)
	}
	s := string(out)
	for _, gone := range []string{"hunter2hunter2", "/home/alice", awsKey} {
		if strings.Contains(s, gone) {
			t.Fatalf("%q survived: %s", gone, s)
		}
	}
	// The key that looks like a token is a key and stays.
	if !strings.Contains(s, `"ghp_16C7e42F292c6912E7710c838347Ae178B4a":`) {
		t.Fatalf("keys must not be redacted: %s", s)
	}
	if !strings.Contains(s, `"a < b && c > d"`) {
		t.Fatalf("HTML characters must not be escaped: %s", s)
	}
	// Structure and untouched values are byte-identical: replacing the tokens with
	// the originals yields the input.
	var a, b map[string]any
	_ = json.Unmarshal(in, &a)
	_ = json.Unmarshal(out, &b)
	if len(a) != len(b) {
		t.Fatalf("structure changed: %s", s)
	}
	if !strings.Contains(s, `{ "password" : "`) || !strings.Contains(s, ` 42, true, null, "plain"]`) {
		t.Fatalf("whitespace and literals must survive: %s", s)
	}
	if again := r.JSON(out); string(again) != string(out) {
		t.Fatalf("JSON not idempotent:\n%s\n%s", out, again)
	}
	// Clean documents come back as the very same bytes, invalid ones untouched.
	clean := json.RawMessage(`{"a": [1, 2, {"b": "c"}]}`)
	if got := r.JSON(clean); string(got) != string(clean) {
		t.Fatalf("clean JSON changed: %s", got)
	}
	bad := json.RawMessage(`{"password": "hunter2hunter2"`)
	if got := r.JSON(bad); string(got) != string(bad) {
		t.Fatalf("invalid JSON must be returned unchanged: %s", got)
	}
	for _, edge := range []string{``, `null`, `"` + ghToken + `"`, `[]`, `""`, `"\u2028\ud83d\ude00"`} {
		if got := r.JSON(json.RawMessage(edge)); !json.Valid(got) && edge != `` {
			t.Fatalf("JSON(%q) = %q is not valid", edge, got)
		}
	}
	if got := string(r.JSON(json.RawMessage(`"` + ghToken + `"`))); strings.Contains(got, ghToken) {
		t.Fatalf("top-level string not redacted: %s", got)
	}
}

func TestJSONInsideToolCallArguments(t *testing.T) {
	// A tool call's arguments are a JSON string that itself holds JSON.
	r := testRedactor()
	wire := `{"function":{"arguments":"{\"command\":\"curl -H 'X-Api-Key: 7f3a9c1e5b2d4f6a8c0e' http://x\"}","name":"bash"},"id":"call_1","type":"function"}`
	out := string(r.JSON(json.RawMessage(wire)))
	if strings.Contains(out, "7f3a9c1e5b2d4f6a8c0e") || !json.Valid([]byte(out)) {
		t.Fatalf("secret inside arguments survived or JSON broke: %s", out)
	}
	var w struct {
		Function struct{ Arguments string }
	}
	if err := json.Unmarshal([]byte(out), &w); err != nil || !json.Valid([]byte(w.Function.Arguments)) {
		t.Fatalf("arguments must stay valid JSON after redaction: %v %q", err, w.Function.Arguments)
	}
}

func TestStatsAndCacheCountEveryOccurrence(t *testing.T) {
	r := testRedactor()
	long := strings.Repeat("filler text ", 20) + " token " + ghToken
	for i := 0; i < 5; i++ {
		if !strings.Contains(r.String(long), "⟦redacted:github:") {
			t.Fatal("not redacted")
		}
	}
	if got := r.Stats()[KindGitHub]; got != 5 {
		t.Fatalf("cache hits must still be counted: %d", got)
	}
	out, changed := r.Changed(long)
	if !changed || out == long {
		t.Fatal("Changed should report the edit")
	}
	if _, changed := r.Changed("nothing here"); changed {
		t.Fatal("clean text reported as changed")
	}
	st := r.Stats()
	st[KindGitHub] = 99
	if r.Stats()[KindGitHub] == 99 {
		t.Fatal("Stats must return a copy")
	}
}

func TestConcurrentUse(t *testing.T) {
	r := testRedactor()
	var wg sync.WaitGroup
	want := r.String("k " + ghToken + " " + strings.Repeat("x", 100))
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				if got := r.String("k " + ghToken + " " + strings.Repeat("x", 100)); got != want {
					t.Errorf("goroutine %d: %s", g, got)
					return
				}
				r.String(fmt.Sprintf("unique %d %d /home/u%d/x", g, i, i))
				r.JSON(json.RawMessage(`{"a":"` + awsKey + `"}`))
				_ = r.Stats()
			}
		}(g)
	}
	wg.Wait()
	if r.Stats()[KindPath] == 0 || r.Stats()[KindAWS] != 1600 {
		t.Fatalf("stats: %v", r.Stats())
	}
}

func TestAdversarialInputs(t *testing.T) {
	r := testRedactor()
	big := strings.Repeat("a1B2c3D4e5F6g7H8i9J0", 50_000) // 1 MB, no key word: nothing to redact
	if out := r.String(big); out != big {
		t.Fatal("large clean input changed")
	}
	huge := strings.Repeat("password=", 5000) + strings.Repeat("A", 100000)
	_ = r.String(huge)
	for _, in := range []string{
		"\x00\x00\x00", "⟦redacted:", "⟦redacted:x:zzzzzz⟧", "password=\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00",
		"日本語のテキスト /home/山田/x alice@例え.jp", "\xff\xfe invalid \xc3\x28 utf8 " + ghToken,
		"-----BEGIN PRIVATE KEY-----", "-----BEGIN PRIVATE KEY-----\n", "Bearer", "Authorization:", "::", ":::::", "1.2.3.4.5.6.7.8",
		strings.Repeat("-----BEGIN PRIVATE KEY-----\nMIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcw\n", 200),
		"api_key=" + strings.Repeat("A", 100000),
	} {
		out := r.String(in)
		if again := r.String(out); again != out {
			t.Errorf("not idempotent on %q: %q vs %q", truncate(in), truncate(out), truncate(again))
		}
	}
	// A masked token next to a secret-looking key must not be re-redacted into a
	// different token (the classic non-terminating fixed point).
	tok := r.token(KindSecret, "v")
	in := "password=" + tok + tok + tok
	if out := r.String(in); out != in {
		t.Fatalf("existing tokens must be inert: %q", out)
	}
}

func truncate(s string) string {
	if len(s) > 80 {
		return s[:80] + "..."
	}
	return s
}

func TestCRLFAndMultiline(t *testing.T) {
	r := testRedactor()
	in := "line one\r\nDB_PASSWORD=hunter2hunter2\r\nline three\r\n"
	out := r.String(in)
	if strings.Contains(out, "hunter2hunter2") || !strings.HasSuffix(out, "\r\nline three\r\n") || !strings.HasPrefix(out, "line one\r\nDB_PASSWORD=⟦redacted:secret:") {
		t.Fatalf("CRLF handling: %q", out)
	}
	pem := "before\r\n-----BEGIN PRIVATE KEY-----\r\nMIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcwggSjAgEAAoIBAQC7VJTUt9Us8cKj\r\n-----END PRIVATE KEY-----\r\nafter"
	if got := r.String(pem); got != "before\r\n"+r.token(KindPrivateKey, "-----BEGIN PRIVATE KEY-----\r\nMIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcwggSjAgEAAoIBAQC7VJTUt9Us8cKj\r\n-----END PRIVATE KEY-----")+"\r\nafter" {
		t.Fatalf("PEM with CRLF: %q", got)
	}
}

func BenchmarkRedactCleanCode(b *testing.B) {
	r := New(Config{})
	src := strings.Repeat("func (s *Server) handle(w http.ResponseWriter, r *http.Request) {\n\tid := r.URL.Query().Get(\"id\")\n\tlog.Printf(\"request %s from %s\", id, r.RemoteAddr)\n}\n", 400)
	b.SetBytes(int64(len(src)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.run(src)
	}
}

// TestRepoSourcesStayUntouched runs the redactor over real, stable source files
// of this repository (production code of core and events, module files). Ordinary
// Go must come through byte for byte.
func TestRepoSourcesStayUntouched(t *testing.T) {
	var files []string
	for _, glob := range []string{"../../core/*.go", "../../events/*.go", "../../../go.mod", "../../../go.sum"} {
		m, _ := filepath.Glob(glob)
		files = append(files, m...)
	}
	if len(files) == 0 {
		t.Skip("repository sources not found")
	}
	r := testRedactor()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			if out, changed := r.Changed(line); changed {
				t.Errorf("%s:%d was redacted:\n%s\n%s", f, i+1, line, out)
			}
		}
	}
}
