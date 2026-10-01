package redact

import (
	"math"
	"net/netip"
	"strings"
	"testing"
)

// One table per rule: inputs that rule must redact, with the secret that must be gone,
// and inputs it must leave alone. Each rule runs on its own (Config.Kinds), so a negative
// says something about that rule, not about another one that would catch the text first;
// a positive is then also run with every rule on. Every credential-shaped value is
// assembled at run time (see frag): no line of this file is shaped like a secret.
type ruleCase struct{ name, in, secret string }

type ruleCases struct {
	pos []ruleCase
	neg []ruleCase // secret is unused
}

func neg(name, in string) ruleCase { return ruleCase{name: name, in: in} }

var (
	awsSTS    = frag("AS", "IA", "ABCD1234EFGH5678")
	awsRole   = frag("AR", "OA", "ABCD1234EFGH5678")
	slackApp  = frag("xa", "pp-1-", "A0123456789abcdef")
	stripeRk  = frag("rk", "_live_", "51H8xQwErTyUiOpAsDfGhJkLz")
	classicSk = frag("s", "k-", "abcdEFGH1234ijklMNOP5678qrstUVWX9012yzAB")
	certBlock = frag("-----", "BEGIN CERTIFICATE", "-----\n") + pemBody + frag("\n-----", "END CERTIFICATE", "-----")
	pubBlock  = frag("-----", "BEGIN PUBLIC KEY", "-----\n") + pemBody + frag("\n-----", "END PUBLIC KEY", "-----")
)

var ruleTable = map[string]ruleCases{
	KindPrivateKey: {
		pos: []ruleCase{
			{"rsa block", "key:\n" + pemFull + "\nend", "QUJDREVGR0hJSktMTU5PUFFSU1RVVldYWVo="},
			{"ec block", pemBegin("EC ") + "\n" + pemBody + "\n" + pemEnd("EC "), "YWJjZGVmZ2hpamtsbW5vcHFyc3R1dnd4eXo="},
			{"pkcs8 block", pemBegin("") + "\n" + pemBody + "\n" + pemEnd(""), "QUJDREVGR0hJSktMTU5PUFFSU1RVVldYWVo="},
			{"encrypted block with headers", pemBegin("RSA ") + "\nProc-Type: 4,ENCRYPTED\nDEK-Info: AES-128-CBC,0123456789ABCDEF\n\n" + pemBody + "\n" + pemEnd("RSA "), "QUJDREVGR0hJSktMTU5PUFFSU1RVVldYWVo="},
			{"block cut short by truncation", pemBegin("") + "\n" + pemBody + "\n… [4000 chars elided] …", "QUJDREVGR0hJSktMTU5PUFFSU1RVVldYWVo="},
			{"CRLF line ends", pemBegin("") + "\r\n" + pemBody + "\r\n" + pemEnd(""), "QUJDREVGR0hJSktMTU5PUFFSU1RVVldYWVo="},
		},
		neg: []ruleCase{
			neg("a mention in code", `if !strings.Contains(s, "`+pemBegin("RSA ")+`") { return }`),
			neg("a mention in prose", "The file starts with "+pemBegin("")+" and then base64."),
			neg("a begin line and nothing after it", pemBegin("")+"\n"),
			neg("a certificate", certBlock),
			neg("a public key", pubBlock),
			neg("the word private key", "keep the private key somewhere safe"),
		},
	},
	KindJWT: {
		pos: []ruleCase{
			{"a three-part token", "cookie: session=" + jwtTok, jwtTok},
			{"in a header", "Authorization: " + jwtTok, jwtTok},
			{"an unsigned token", frag("ey", "JhbGciOiJub25lIn0", ".", "ey", "JzdWIiOiIxMjM0NTY3ODkwIn0", "."), frag("ey", "JhbGciOiJub25lIn0")},
		},
		neg: []ruleCase{
			neg("only a header", frag("ey", "JhbGciOiJIUzI1NiJ9")),
			neg("two parts", frag("ey", "JhbGciOiJIUzI1NiJ9", ".", "ey", "JzdWIiOiIxMjM0NTY3ODkwIn0")),
			neg("a short base64 that starts alike", frag("ey", "Jabc.", "ey", "Jabc.x")),
			neg("prose", "the token starts with the letters e, y, J"),
		},
	},
	KindAWS: {
		pos: []ruleCase{
			{"an access key id", "aws_access_key_id = " + awsKey, awsKey},
			{"a session key id", "export AWS_ACCESS_KEY_ID=" + awsSTS, awsSTS},
			{"a role key id", "role " + awsRole + " assumed", awsRole},
			{"in json", `{"AccessKeyId":"` + awsKey + `"}`, awsKey},
		},
		neg: []ruleCase{
			neg("lower case", strings.ToLower(awsKey)),
			neg("too short", frag("AK", "IA", "1234")),
			neg("one character short", frag("AK", "IA", "ABCD1234EFGH567")),
			neg("one character long", awsKey+"X"),
			neg("another prefix", frag("AB", "CD", "ABCD1234EFGH5678")),
			neg("a word", "AKIAS is not a key"),
		},
	},
	KindGitHub: {
		pos: []ruleCase{
			{"a classic token", "git clone https://" + ghToken + "@github.com/o/r", ghToken},
			{"a fine-grained token", "token " + ghFine, ghFine},
			{"an OAuth token", frag("gh", "o_", "16C7e42F292c6912E7710c838347Ae178B4a"), frag("gh", "o_", "16C7e42F292c6912E7710c838347Ae178B4a")},
		},
		neg: []ruleCase{
			neg("the prefix alone", frag("gh", "p_")),
			neg("a short suffix", frag("gh", "p_", strings.Repeat("a", 35))),
			neg("a short fine-grained token", frag("github", "_pat_", strings.Repeat("a", 21))),
			neg("another letter", frag("gh", "x_", strings.Repeat("a", 40))),
			neg("a word", "the ghp command"),
		},
	},
	KindGitLab: {
		pos: []ruleCase{
			{"a personal access token", "GITLAB=" + glToken, glToken},
			{"in a url", "https://oauth2:" + glToken + "@gitlab.com/x.git", glToken},
		},
		neg: []ruleCase{
			neg("a short token", frag("glp", "at-", "short")),
			neg("the prefix alone", frag("glp", "at-")),
			neg("another prefix", frag("glp", "xt-", strings.Repeat("a", 24))),
		},
	},
	KindSlack: {
		pos: []ruleCase{
			{"a bot token", "SLACK_BOT=" + slackTok, slackTok},
			{"an app token", "app " + slackApp, slackApp},
			{"a webhook", "curl " + slackHook, "AbCdEfGhIjKlMnOpQrStUvWx"},
		},
		neg: []ruleCase{
			neg("a short token", frag("xo", "xb-", "short")),
			neg("an unknown kind", frag("xo", "xz-", "1234567890abcdef")),
			neg("a webhook url without ids", "https://hooks.slack.com/services/"),
			neg("prose", "the xox prefix is used by slack"),
		},
	},
	KindGoogle: {
		pos: []ruleCase{
			{"an api key", "key=" + googleKey, googleKey},
			{"in a url", "https://maps.example/api?key=" + googleKey + "&q=x", googleKey},
		},
		neg: []ruleCase{
			neg("one character short", frag("AI", "za", strings.Repeat("a", 34))),
			neg("one character long", frag("AI", "za", strings.Repeat("a", 36))),
			neg("another prefix", frag("AI", "zb", strings.Repeat("a", 35))),
		},
	},
	KindStripe: {
		pos: []ruleCase{
			{"a live secret key", "STRIPE " + stripeKey, stripeKey},
			{"a test secret key", stripeTest, stripeTest},
			{"a restricted key", stripeRk, stripeRk},
		},
		neg: []ruleCase{
			neg("a publishable key", frag("pk", "_live_", "51H8xQwErTyUiOpAsDfGhJkLz")),
			neg("a short key", frag("sk", "_live_", "short")),
			neg("another mode", frag("sk", "_prod_", strings.Repeat("a", 24))),
		},
	},
	KindNPM: {
		pos: []ruleCase{
			{"a token", "//registry.npmjs.org/:_authToken=" + npmToken, npmToken},
			{"a bare token", npmToken, npmToken},
		},
		neg: []ruleCase{
			neg("a short token", frag("np", "m_", strings.Repeat("a", 35))),
			neg("a config variable", "npm_config_registry=https://registry.npmjs.org"),
			neg("the command", "run npm install first"),
		},
	},
	KindPyPI: {
		pos: []ruleCase{
			{"an upload token", "password: " + pypiToken, pypiToken},
			{"in a config file", "[pypi]\nusername = __token__\npassword = " + pypiToken, pypiToken},
		},
		neg: []ruleCase{
			neg("a short token", frag("py", "pi-", "AgEIcHlwaS5vcmc", "shorttail")),
			neg("an index url", "--index-url https://pypi.org/simple"),
			neg("a package name", "pypi-simple-index-helper"),
		},
	},
	KindLLM: {
		pos: []ruleCase{
			{"an anthropic key", "export ANTHROPIC_API_KEY=" + llmKey, llmKey},
			{"a project key", "OPENAI=" + openaiKey, openaiKey},
			{"a classic key", "key " + classicSk, classicSk},
		},
		neg: []ruleCase{
			neg("a slug without digits", "https://scikit.example/"+frag("s", "k-", "learn-machine-learning-guide-xx")),
			neg("a short key", frag("s", "k-", "abc123")),
			neg("a word that ends alike", frag("see /docs/ri", "sk", "-management-strategy-for-the-year-2024-planning")),
		},
	},
	KindHuggingFace: {
		pos: []ruleCase{
			{"a token", "HF_TOKEN=" + hfToken, hfToken},
			{"in a header", "Authorization: Bearer " + hfToken, hfToken},
		},
		neg: []ruleCase{
			neg("a short token", frag("h", "f_", strings.Repeat("a", 33))),
			neg("a function", "from huggingface_hub import hf_hub_download"),
			neg("glued to a word", frag("s", "hf_", strings.Repeat("a", 40))),
		},
	},
	KindURLCred: {
		pos: []ruleCase{
			{"a password in a clone url", "git clone https://deploy:s3cr3tPassw0rd@git.corp.io/x.git", "s3cr3tPassw0rd"},
			{"a database url", "postgres://admin:Xk9mQ2vL7p@db.internal:5432/app", "Xk9mQ2vL7p"},
			{"an encoded password", "ftp://u:P%40ssw0rd%21@files.example.org/", "P%40ssw0rd%21"},
		},
		neg: []ruleCase{
			neg("the same user and password", "postgres://postgres:postgres@localhost:5432/db"),
			neg("a password that is the word", "redis://default:password@cache:6379"),
			neg("a variable", "https://user:${PASSWORD}@host.example/x"),
			neg("a placeholder", "https://user:<your-password>@host.example/x"),
			neg("a url with no credentials", "https://github.com/anemos-labs/sleipnir/issues/12"),
			neg("a user and no password", "https://token@github.com/x/y"),
			neg("a password that is too short", "https://user:pw@host.example/x"),
		},
	},
	KindBearer: {
		pos: []ruleCase{
			{"a bearer header", `curl -H "Authorization: Bearer 3f9a8b7c6d5e4f3a2b1c0d9e8f7a6b5c" https://api.x`, "3f9a8b7c6d5e4f3a2b1c0d9e8f7a6b5c"},
			{"a basic header", "Authorization: Basic dXNlcjpzdXBlcnNlY3JldHBhc3M=", "dXNlcjpzdXBlcnNlY3JldHBhc3M="},
			{"a proxy header", "Proxy-Authorization: Bearer 9f8e7d6c5b4a39281706f5e4d3c2b1a0", "9f8e7d6c5b4a39281706f5e4d3c2b1a0"},
			{"a bare bearer token", "got Bearer abcDEF123456ghiJKL789012mno back", "abcDEF123456ghiJKL789012mno"},
		},
		neg: []ruleCase{
			neg("a variable", `curl -H "Authorization: Bearer $TOKEN" https://x`),
			neg("a concatenation", `headers["Authorization"] = "Bearer " + accessToken`),
			neg("a placeholder", "Authorization: Bearer <your-token-here>"),
			neg("a short value", "Authorization: Bearer abc"),
			neg("the scheme alone", "Authorization: Bearer"),
			neg("prose about bearer tokens", "bearer tokens authentication scheme is described in RFC 6750"),
			neg("a bare bearer word run", "Bearer abcdefghijklmnopqrstuvwxyz"),
		},
	},
	KindSecret: {
		pos: []ruleCase{
			{"a dotenv password", "DB_PASSWORD=hunter2hunter2", "hunter2hunter2"},
			{"a dotenv value of letters", "SECRET_KEY=mysupersecretkeyvalue", "mysupersecretkeyvalue"},
			{"a yaml key", "api_key: 7f3a9c1e5b2d4f6a8c0e", "7f3a9c1e5b2d4f6a8c0e"},
			{"a json member", `{"client_secret": "a1b2c3d4e5f6g7h8i9j0"}`, "a1b2c3d4e5f6g7h8i9j0"},
			{"a go assignment", `dbPassword := "correct-horse-battery-staple-9"`, "correct-horse-battery-staple-9"},
			{"a php array", `'password' => 'xK3$9mQ2pL7vN4'`, "xK3$9mQ2pL7vN4"},
			{"a connection string", "Server=db;User Id=sa;Password=P4ssw0rd!Long1;Database=x", "P4ssw0rd!Long1"},
			{"a flag with a space", "mysql --password Sup3rS3cret!pw -u root", "Sup3rS3cret!pw"},
			{"a flag with an equals sign", "tool --token=abc123def456ghi789", "abc123def456ghi789"},
			{"a header", `-H "X-Api-Key: 7f3a9c1e5b2d4f6a8c0e"`, "7f3a9c1e5b2d4f6a8c0e"},
		},
		neg: []ruleCase{
			neg("an environment variable", "password: ${DB_PASSWORD}"),
			neg("an environment lookup", `password := os.Getenv("DB_PASSWORD")`),
			neg("a template", "password: {{ .Values.database.password }}"),
			neg("a number", "token: 12345678901234567890"),
			neg("a name", `token = "access_token_value_name"`),
			neg("a url", `token_url = "https://example.com/oauth/token/endpoint"`),
			neg("a path", `secret = "/run/secrets/db_password_file"`),
			neg("a secret name", `secret_id = "prod/database/password-store"`),
			neg("a sentence", `"password": "Enter your password to continue"`),
			neg("a placeholder", "api_key: your_api_key_goes_here_please"),
			neg("a keyword that does not end the name", "max_tokens = 4096000000000"),
			neg("a constant", "const tokenLength = 32"),
			neg("a flag without a value", "--password is required for this command"),
			neg("a short value", "password=abc"),
		},
	},
	KindEntropy: {
		pos: []ruleCase{
			{"after a key word in prose", "the api key is Zq8Yw3Xe6Rt1Uy4Io7Pa0Sd2Fg5Hj9Kl, keep it safe", "Zq8Yw3Xe6Rt1Uy4Io7Pa0Sd2Fg5Hj9Kl"},
			{"after a header word", "auth Zq8Yw3Xe6Rt1Uy4Io7Pa0Sd2Fg5Hj9Kl", "Zq8Yw3Xe6Rt1Uy4Io7Pa0Sd2Fg5Hj9Kl"},
			{"after a secret name", "the secret_key_base is kJ8dHs92Lp0QwErTy5UiOp3aSdFgHj7Kl for prod", "kJ8dHs92Lp0QwErTy5UiOp3aSdFgHj7Kl"},
			{"after a camel-case name", `apiKey "Zq8Yw3Xe6Rt1Uy4Io7Pa0Sd2Fg5Hj9Kl"`, "Zq8Yw3Xe6Rt1Uy4Io7Pa0Sd2Fg5Hj9Kl"},
		},
		neg: []ruleCase{
			neg("a blob with no key word", "see Zq8Yw3Xe6Rt1Uy4Io7Pa0Sd2Fg5Hj9Kl here"),
			neg("a key word on another line", "the key\nZq8Yw3Xe6Rt1Uy4Io7Pa0Sd2Fg5Hj9Kl"),
			neg("a git sha", "the key commit is 4b825dc642cb6eb9a060e54bf8d69288fbee4904 in main"),
			neg("a sha256", "token digest e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"),
			neg("a uuid", "the auth token id 550e8400-e29b-41d4-a716-446655440000 expired"),
			neg("an identifier without digits", "auth handleAuthenticationTokenRefreshWithBackoff"),
			neg("a slug with digits", "token my-service-name-2024-prod-eu"),
			neg("a key word too far back", "key "+strings.Repeat("x", 70)+" Zq8Yw3Xe6Rt1Uy4Io7Pa0Sd2Fg5Hj9Kl"),
			neg("a run of digits", "key 12345678901234567890123"),
			neg("a run of letters", "key abcdefghijklmnopqrstuvwxyz"),
		},
	},
	KindEmail: {
		pos: []ruleCase{
			{"in prose", "contact alice.smith@corp-example.io now", "alice.smith@corp-example.io"},
			{"in angle brackets", "From: Bob <bob@acme.dev>", "bob@acme.dev"},
			{"with a plus tag", "carol+news@mail.example.co.uk", "carol+news@mail.example.co.uk"},
			{"in a url parameter", "?email=dave@startup.ai&x=1", "dave@startup.ai"},
		},
		neg: []ruleCase{
			neg("an ssh remote", "git@github.com:anemos-labs/sleipnir.git"),
			neg("a no-reply address", "Co-Authored-By: Claude <noreply@anthropic.com>"),
			neg("an example domain", "reach user@example.com or admin@example.org"),
			neg("a reserved domain", "me@host.invalid and me@box.test and me@pc.localhost"),
			neg("a package spec", "lodash@4.17.21 react@18.2.0 @types/node@22.1.0"),
			neg("a matrix product", "y = W@x.T + b"),
			neg("userinfo in a url", `fetch("https://example.com@evil.org/a")`),
			neg("an image digest", "image@sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"),
		},
	},
	KindIPv4: {
		pos: []ruleCase{
			{"an address with a port", "connect to 54.239.28.85:443", "54.239.28.85"},
			{"in a url", "http://8.8.4.4/dns-query", "8.8.4.4"},
			{"the second of a range", "scan 8.8.8.8-8.8.4.4", "8.8.4.4"},
			{"in a log line", `level=info client="93.184.216.34" ok`, "93.184.216.34"},
		},
		neg: []ruleCase{
			neg("loopback and unspecified", "listen 127.0.0.1:8080 and 0.0.0.0:9090"),
			neg("private ranges", "10.0.0.5 172.16.4.9 192.168.1.20 169.254.169.254 100.64.0.1"),
			neg("documentation ranges", "192.0.2.1 198.51.100.7 203.0.113.99"),
			neg("a netmask", "netmask 255.255.255.0 gateway 255.255.255.255"),
			neg("a browser version", "Mozilla/5.0 Chrome/120.0.0.0 Safari/537.36"),
			neg("a longer dotted number", "1.3.6.1.4.1.311.21.7 and 1.2.3.4.5"),
			neg("a version with a suffix", "release 8.8.8.8-beta and build-9.9.9.9"),
			neg("a network", "allow 8.8.8.0/24"),
			neg("not an address", "999.1.1.1 and 1.2.3.256"),
		},
	},
	KindIPv6: {
		pos: []ruleCase{
			{"in brackets with a port", "listen [2a00:1450:4001:81b::200e]:443", "2a00:1450:4001:81b::200e"},
			{"after a label", "addr:2a00:1450:4001:81b::200e ok", "2a00:1450:4001:81b::200e"},
			{"a full address", "2606:4700:4700:0:0:0:0:1111", "2606:4700:4700:0:0:0:0:1111"},
		},
		neg: []ruleCase{
			neg("loopback, link-local and unique-local", "::1 fe80::1ff:fe23:4567:890a fd12:3456:789a::1"),
			neg("the documentation prefix", "2001:db8::1"),
			neg("c++ scopes", "std::vector<int> a::b Foo::bar dead::beef cache::default"),
			neg("a mac address", "hw aa:bb:cc:dd:ee:ff and 00:1A:2B:3C:4D:5E"),
			neg("a timestamp", "12:34:56 and 10:30:45.123 at 2026-09-30T12:34:56Z"),
			neg("a network", "allow 2a00:1450::/32"),
			neg("an address mapped from v4", "::ffff:8.8.8.8"),
		},
	},
	KindPath: {
		pos: []ruleCase{
			{"a unix home directory", "/home/alice/project/main.go", "alice"},
			{"a mac home directory", "/Users/bob.smith/Library/x", "bob.smith"},
			{"a windows home directory", `C:\Users\Carol\AppData\Local`, "Carol"},
			{"a windows path with slashes", "c:/users/Dave/x", "Dave"},
			{"at the end of a sentence", "see /home/alice.", "alice"},
			{"through a mount", "/mnt/c/Users/erin/x", "erin"},
		},
		neg: []ruleCase{
			neg("the placeholder itself", "/home/user/project and C:\\Users\\user\\x"),
			neg("a shared directory", "/Users/Shared/Library /Users/Public/Documents"),
			neg("a variable or a template", "/home/${USER}/x /home/<name>/y ~/z"),
			neg("a file directly under home", "cat /home/README.md /home/config.yaml"),
			neg("a relative path through a directory called home", "AGENTS.md: @../home/ok.md"),
			neg("system paths", "/usr/local/go/bin /var/lib/docker /etc/ssl/private/key.pem"),
		},
	},
}

func TestEveryRuleHasPositiveAndNegativeCases(t *testing.T) {
	for _, kind := range AllKinds() {
		c, ok := ruleTable[kind]
		if !ok || len(c.pos) == 0 || len(c.neg) == 0 {
			t.Errorf("rule %q needs at least one positive and one negative case in ruleTable", kind)
		}
	}
	for kind := range ruleTable {
		if !validKind(kind) {
			t.Errorf("ruleTable has cases for %q, which is no rule", kind)
		}
	}
}

func TestRulesOneByOne(t *testing.T) {
	for _, kind := range AllKinds() {
		cases := ruleTable[kind]
		t.Run(kind, func(t *testing.T) {
			for _, c := range cases.pos {
				t.Run("redacts "+c.name, func(t *testing.T) {
					alone := New(Config{Salt: "s", Kinds: []string{kind}})
					out := alone.String(c.in)
					if strings.Contains(out, c.secret) {
						t.Fatalf("the secret survived:\n in: %q\nout: %q", c.in, out)
					}
					// Every rule leaves a token of its own kind but the home-directory rule: it swaps the
					// user name for the placeholder, which is no secret and needs no token.
					if kind == KindPath {
						if want := strings.Replace(c.in, c.secret, "user", 1); out != want || alone.Stats()[kind] != 1 {
							t.Fatalf("want the user name swapped for the placeholder:\n in: %q\nout: %q\nwant: %q (stats %v)", c.in, out, want, alone.Stats())
						}
					} else if !strings.Contains(out, "⟦redacted:"+kind+":") || alone.Stats()[kind] == 0 {
						t.Fatalf("want a %s token:\n in: %q\nout: %q (stats %v)", kind, c.in, out, alone.Stats())
					}
					if again := alone.String(out); again != out {
						t.Fatalf("not idempotent:\n1: %q\n2: %q", out, again)
					}
					if out2 := New(Config{Salt: "s"}).String(c.in); strings.Contains(out2, c.secret) {
						t.Fatalf("with every rule on, the secret survived: %q", out2)
					}
				})
			}
			for _, c := range cases.neg {
				t.Run("keeps "+c.name, func(t *testing.T) {
					alone := New(Config{Salt: "s", Kinds: []string{kind}})
					if out := alone.String(c.in); out != c.in || alone.Total() != 0 {
						t.Fatalf("a false positive:\n in: %q\nout: %q (stats %v)", c.in, out, alone.Stats())
					}
				})
			}
		})
	}
}

// Kinds and aliases select rules: a rule that is off leaves its secrets, the token group is the
// provider families, names are case-insensitive, unknown names are reported by Validate and ignored
// by New.
func TestKindSelection(t *testing.T) {
	text := "k " + awsKey + " m alice@corp.io h 8.8.8.8 p /home/carol/x"
	for _, tc := range []struct {
		kinds []string
		gone  []string
		kept  []string
	}{
		{[]string{"aws"}, []string{awsKey}, []string{"alice@corp.io", "8.8.8.8", "carol"}},
		{[]string{"EMAIL", " ipv4 "}, []string{"alice@corp.io", "8.8.8.8"}, []string{awsKey, "carol"}},
		{[]string{GroupTokens}, []string{awsKey}, []string{"alice@corp.io", "8.8.8.8", "carol"}},
		{[]string{"nonsense"}, nil, []string{awsKey, "alice@corp.io", "8.8.8.8", "carol"}},
		{[]string{}, nil, []string{awsKey, "alice@corp.io", "8.8.8.8", "carol"}},
		{nil, []string{awsKey, "alice@corp.io", "8.8.8.8", "carol"}, nil},
	} {
		out := New(Config{Kinds: tc.kinds}).String(text)
		for _, g := range tc.gone {
			if strings.Contains(out, g) {
				t.Errorf("kinds %q: %q survived in %q", tc.kinds, g, out)
			}
		}
		for _, k := range tc.kept {
			if !strings.Contains(out, k) {
				t.Errorf("kinds %q: %q was redacted in %q", tc.kinds, k, out)
			}
		}
	}
	if err := (Config{Kinds: []string{"aws", "bogus", "TOKENS"}}).Validate(); err == nil || !strings.Contains(err.Error(), "bogus") || strings.Contains(err.Error(), "TOKENS,") {
		t.Errorf("Validate = %v", err)
	}
	if err := (Config{Kinds: []string{"aws", " Email ", "tokens"}}).Validate(); err != nil {
		t.Errorf("Validate of valid kinds: %v", err)
	}
	for _, k := range tokenFamilies {
		if !validKind(k) {
			t.Errorf("token family %q is no rule", k)
		}
	}
}

func TestRuleHelpers(t *testing.T) {
	check := func(t *testing.T, name string, f func(string) bool, yes, no []string) {
		t.Helper()
		for _, s := range yes {
			if !f(s) {
				t.Errorf("%s(%q) = false, want true", name, s)
			}
		}
		for _, s := range no {
			if f(s) {
				t.Errorf("%s(%q) = true, want false", name, s)
			}
		}
	}
	t.Run("isReference", func(t *testing.T) {
		check(t, "isReference", isReference,
			[]string{"", "${VAR}", "$(cmd)", "{{ .Values.x }}", "%(name)s", "#{x}", "<%= x %>", "{x}", "<name>", "[x]", "(x)", "$TOKEN", "%s", "@ivar", "&anchor", "*alias", "!tag",
				"|", ">", "os.Getenv(x)", "env.X", "process.env.X", "vault:secret/x", "file:///etc/x", "a{{b", "x${y}z", "CONFIG.KEY"},
			[]string{"hunter2hunter2", "$ecr3t!Pass#word", "Tr0ub4dor&3", "abc@def", "100%sure"})
	})
	t.Run("isPlaceholder", func(t *testing.T) {
		check(t, "isPlaceholder", isPlaceholder,
			[]string{"your_key_here", "YOUR-TOKEN", "insert_token", "<anything>", "changeme123", "ReplaceMe", "xxxxxxxxxxxx", "************", "aaaaaaaaaaaa", "my-placeholder-value", "REDACTED-x", "a......b", ""},
			[]string{"hunter2hunter2", "Tr0ub4dor&3", "abcabc"})
	})
	t.Run("isNumeric", func(t *testing.T) {
		check(t, "isNumeric", isNumeric, []string{"0", "12345678901234567890"}, []string{"", "12a", "1.5", "-1", "١٢٣"})
	})
	t.Run("isURLWithoutCreds", func(t *testing.T) {
		check(t, "isURLWithoutCreds", isURLWithoutCreds, []string{"https://x.org/a", "HTTP://X", "ws://h", "wss://h/p"}, []string{"https://u:p@x.org", "ftp://x", "x.org", ""})
	})
	t.Run("looksLikePath", func(t *testing.T) {
		check(t, "looksLikePath", looksLikePath,
			[]string{"/etc/x", "./x", "../x", "~/x", `\\srv\x`, `C:\x`, "c:/x", "prod/db/password"},
			[]string{"Ab/Cd+Ef=", "abc", "a/b+c", "AB/CD", "", "x"})
	})
	t.Run("isIdentifierLike", func(t *testing.T) {
		check(t, "isIdentifierLike", isIdentifierLike,
			[]string{"access_token", "my-service-name", "a.b.c", "getUserById", "GetUserById", "ABC_DEF"},
			[]string{"abc123", "Zq8Yw3Xe6Rt", "hunter", "a_1", "", "getUser1"})
	})
	t.Run("isWordy", func(t *testing.T) {
		check(t, "isWordy", isWordy,
			[]string{"getUserByIdV2AndValidateSession", "my-service-name-2024-prod-eu", "accesstokenvalue1"},
			[]string{"Zq8Yw3Xe6Rt1Uy4Io7Pa0Sd2Fg5Hj9Kl", "", "1234567890", "a1b2c3d4"})
	})
	t.Run("isHex", func(t *testing.T) {
		check(t, "isHex", isHex, []string{"deadbeef", "DEADBEEF01", "0"}, []string{"", "xyz", "dead beef", "deadbeeg"})
	})
	t.Run("isLettersOnly", func(t *testing.T) {
		check(t, "isLettersOnly", isLettersOnly, []string{"abc", "日本語", "Ünï"}, []string{"", "ab1", "a b", "a_b"})
	})
	t.Run("fileLike", func(t *testing.T) {
		check(t, "fileLike", fileLike, []string{"README.md", "a.GO", "x.tar.gz", "id_rsa.pem"}, []string{"", ".env", "noext", "a.unknownext", "archive."})
	})
	t.Run("containsFold", func(t *testing.T) {
		check(t, "containsFold", func(s string) bool { return containsFold(s, "authorization") },
			[]string{"authorization", "AUTHORIZATION", "Authorization", "x-Authorization: y"}, []string{"", "aUtHoRiZaTiOn", "author"})
	})
	t.Run("credentialValueOK", func(t *testing.T) {
		check(t, "credentialValueOK", credentialValueOK,
			[]string{"3f9a8b7c6d5e4f3a2b1c0d9e8f7a6b5c", "dXNlcjpzdXBlcnNlY3JldHBhc3M=", "abc12345xyz"},
			[]string{"", "short", "${TOKEN_VALUE}", "<your-token-here>", "access_token", "required", "REQUIRED", "x...........y", "nullnull"[:4]})
	})
	t.Run("entropyCandidateOK", func(t *testing.T) {
		check(t, "entropyCandidateOK", entropyCandidateOK,
			[]string{"Zq8Yw3Xe6Rt1Uy4Io7Pa0Sd2Fg5Hj9Kl", "kJ8dHs92Lp0QwErTy5UiOp3aSdFgHj7Kl"},
			[]string{"abcdefghijklmnopqrstuvwx", "12345678901234567890", "deadbeefdeadbeef0123", "550e8400-e29b-41d4-a716-446655440000",
				"access_token_value_1", "getUserByIdV2AndValidateSession", "aaaaaaaaaaaaaaaaaaa1", "a/b/c/d/e/f1g2h3i4j5k6"})
	})
	t.Run("urlPasswordOK", func(t *testing.T) {
		for _, tc := range []struct {
			user, pass string
			want       bool
		}{
			{"deploy", "s3cr3tPassw0rd", true}, {"admin", "Xk9mQ2vL7p", true},
			{"postgres", "postgres", false}, {"Admin", "admin", false}, {"u", "password", false}, {"u", "Changeme", false}, {"u", "${X}", false},
			{"u", "<pw>", false}, {"u", "guest", false}, {"u", "xxxxxxxx", false},
		} {
			if got := urlPasswordOK(tc.user, tc.pass); got != tc.want {
				t.Errorf("urlPasswordOK(%q, %q) = %v, want %v", tc.user, tc.pass, got, tc.want)
			}
		}
	})
	t.Run("emailOK", func(t *testing.T) {
		check(t, "emailOK", emailOK,
			[]string{"alice@corp.io", "ab@c.io", "bob@acme.dev", "x.y@mail.example.co.uk", "carol+n@ok.com"},
			[]string{"git@github.com", "noreply@anthropic.com", "No-Reply@x.com", "a@host.invalid", "x@y.test", "me@example.com", "me@sub.example.org", "a@b.shape", "ab@host.shape", "ssh@host.io"})
	})
	t.Run("kvValueOK", func(t *testing.T) {
		for _, tc := range []struct {
			v, key, sep string
			quoted      bool
			want        bool
		}{
			{"Tr0ub4dor&3xyz", "password", ":", true, true},
			{"hunter2hunter2", "DB_PASSWORD", "=", false, true},
			{"mysupersecretkeyvalue", "SECRET_KEY", "=", false, true},
			{"mysupersecretkeyvalue", "secretKey", "=", false, false}, // a variable in code
			{"mysupersecretkeyvalue", "SECRET_KEY", ":", false, false},
			{"short", "password", "=", false, false},
			{"${DB_PASSWORD_XXX}", "password", "=", false, false},
			{"12345678901234567890", "token", ":", false, false},
			{"https://example.com/oauth/token", "token", "=", true, false},
			{"/run/secrets/x_file_y", "secret", "=", true, false},
			{"access_token_value_name", "token", "=", true, false},
			{"Enter your password here", "password", ":", true, false},
			{"foo(bar)baz123456", "password", "=", false, false},
			{"a.b.c.d.e.f.g.h.i.j", "password", "=", false, false},
			{"some->", "password", "=", false, false},
			{"日本語のパスワードです十二文字", "password", "=", true, true},
		} {
			if got := kvValueOK(tc.v, tc.quoted, tc.key, tc.sep); got != tc.want {
				t.Errorf("kvValueOK(%q, quoted=%v, %q, %q) = %v, want %v", tc.v, tc.quoted, tc.key, tc.sep, got, tc.want)
			}
		}
	})
	t.Run("publicV4", func(t *testing.T) {
		for _, tc := range []struct {
			addr   string
			public bool
		}{
			{"8.8.8.8", true}, {"54.239.28.85", true}, {"172.32.0.1", true}, {"1.0.0.1", true},
			{"10.1.2.3", false}, {"127.0.0.1", false}, {"192.168.0.1", false}, {"172.16.0.1", false}, {"172.31.255.255", false}, {"100.64.0.1", false},
			{"169.254.1.1", false}, {"224.0.0.1", false}, {"255.255.255.255", false}, {"0.1.2.3", false}, {"203.0.113.5", false}, {"198.18.0.1", false},
			{"::1", false}, {"2a00:1450::1", false},
		} {
			if got := publicV4(netip.MustParseAddr(tc.addr)); got != tc.public {
				t.Errorf("publicV4(%s) = %v, want %v", tc.addr, got, tc.public)
			}
		}
	})
	t.Run("shannon", func(t *testing.T) {
		for _, tc := range []struct {
			s    string
			want float64
		}{{"", 0}, {"aaaa", 0}, {"ab", 1}, {"abcd", 2}, {"aabb", 1}, {"abcdefgh", 3}} {
			if got := shannon(tc.s); math.Abs(got-tc.want) > 1e-9 {
				t.Errorf("shannon(%q) = %v, want %v", tc.s, got, tc.want)
			}
		}
	})
	t.Run("character classes", func(t *testing.T) {
		for b := 0; b < 256; b++ {
			c := byte(b)
			wantDigit := c >= '0' && c <= '9'
			wantLetter := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
			if isDigit(c) != wantDigit || isLetter(c) != wantLetter || isAlnum(c) != (wantDigit || wantLetter) {
				t.Errorf("class of %#x: digit %v letter %v alnum %v", c, isDigit(c), isLetter(c), isAlnum(c))
			}
			wantV6 := wantDigit || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F' || c == ':' || c == '.'
			if isV6Char(c) != wantV6 {
				t.Errorf("isV6Char(%#x) = %v", c, isV6Char(c))
			}
		}
	})
}

// A rule that is on finds every secret of its kind in a text, however many, and leaves the
// text between them as it was.
func TestEveryOccurrenceIsRedactedAndTheRestIsKept(t *testing.T) {
	for _, kind := range AllKinds() {
		for _, c := range ruleTable[kind].pos {
			if kind == KindPrivateKey || kind == KindEntropy { // spans that depend on their surroundings
				continue
			}
			r := New(Config{Salt: "s", Kinds: []string{kind}})
			in := "BEFORE " + c.in + "\nMIDDLE " + c.in + " AFTER"
			out := r.String(in)
			if strings.Contains(out, c.secret) || !strings.HasPrefix(out, "BEFORE ") || !strings.HasSuffix(out, " AFTER") || !strings.Contains(out, "\nMIDDLE ") {
				t.Errorf("%s/%s: %q -> %q", kind, c.name, in, out)
			}
			if got := r.Stats()[kind]; got < 2 {
				t.Errorf("%s/%s: %d redactions in a text with the secret twice", kind, c.name, got)
			}
		}
	}
}
