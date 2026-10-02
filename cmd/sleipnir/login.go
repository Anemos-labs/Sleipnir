package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"golang.org/x/term"

	"github.com/anemos-labs/sleipnir/internal/chatgptauth"
	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/harden"
	"github.com/anemos-labs/sleipnir/internal/session"
)

func init() {
	extraCommands["login"] = cmdLogin
	extraCommands["logout"] = cmdLogout
}

// loginChoice is a provider that needs a key.
type loginChoice struct{ name, env string }

// loginChoices are the providers that take a key, Heimdall first (it is the recommended one), then the others in alphabetical order.
func loginChoices(cfg *config.Config) []loginChoice {
	var out []loginChoice
	for _, n := range session.ProviderNames(cfg) {
		if _, env, ok := session.ProviderInfo(cfg, n); ok && env != "" {
			out = append(out, loginChoice{n, env})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if (out[i].name == "heimdall") != (out[j].name == "heimdall") {
			return out[i].name == "heimdall"
		}
		return out[i].name < out[j].name
	})
	return out
}

// readSecret reads a key from the terminal without echoing it; from a pipe, one line.
func readSecret(in *bufio.Reader) (string, error) {
	if fd := int(os.Stdin.Fd()); term.IsTerminal(fd) {
		b, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		return strings.TrimSpace(string(b)), err
	}
	line, err := in.ReadString('\n')
	if line == "" && err != nil {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// chatgptName is the provider that signs in with a browser, with no key: the person's ChatGPT plan.
const chatgptName = "chatgpt"

// loginName checks a provider named for login: one that takes a key, or the ChatGPT plan. No name is fine: the menu asks.
func loginName(cfg *config.Config, name string) error {
	if name == "" || strings.EqualFold(name, chatgptName) {
		return nil
	}
	choices := loginChoices(cfg)
	for _, c := range choices {
		if c.name == strings.ToLower(name) {
			return nil
		}
	}
	return fmt.Errorf("login: %q is not a provider that takes a key (those that do: %s; chatgpt signs in with a browser; a local server needs none)", name, joinNames(choices))
}

// login asks which provider (unless named), reads its key, and stores it in ~/.sleipnir/auth.json and in this process. It returns the
// provider's name. A server on this machine needs no key, and a ChatGPT plan is signed in with the browser: both are offered after the
// providers that take a key.
func login(ctx context.Context, in *bufio.Reader, out io.Writer, secret func() (string, error), cfg *config.Config, name string, local []modelSource) (string, error) {
	choices := loginChoices(cfg)
	var pick *loginChoice
	for i := range choices {
		if choices[i].name == strings.ToLower(name) {
			pick = &choices[i]
		}
	}
	if err := loginName(cfg, name); err != nil {
		return "", err
	}
	if strings.EqualFold(name, chatgptName) {
		return chatgptName, signInChatGPT(ctx, in, out)
	}
	extras := []modelSource{{name: chatgptName, plan: true}}
	extras = append(extras, local...)
	describe := func(e modelSource) string {
		if e.plan {
			return e.name + "  (your ChatGPT plan: sign in with the browser, no key)"
		}
		return e.name + "  (running on this machine, no key)"
	}
	chooseExtra := func(i int) (string, error) {
		if extras[i].plan {
			return chatgptName, signInChatGPT(ctx, in, out)
		}
		return extras[i].name, nil
	}
	if pick == nil && arrowOK() {
		var labels []string
		for _, c := range choices {
			if c.name == "heimdall" {
				c.name += "  (recommended)"
			}
			labels = append(labels, c.name)
		}
		for _, e := range extras {
			labels = append(labels, describe(e))
		}
		i, err := selectRows(in, out, "Which provider will you use?", labels, labels, true, pickRows) // thirty or so: typing narrows them
		if err != nil {
			return "", errors.New("login: cancelled")
		}
		if i >= len(choices) {
			return chooseExtra(i - len(choices))
		}
		pick = &choices[i]
	}
	for pick == nil {
		fmt.Fprintln(out, "Which provider will you use?")
		for i, c := range choices {
			note := ""
			if c.name == "heimdall" {
				note = "  (recommended)"
			}
			fmt.Fprintf(out, "  %2d. %s%s\n", i+1, c.name, note)
		}
		for i, e := range extras {
			fmt.Fprintf(out, "  %2d. %s\n", len(choices)+i+1, describe(e))
		}
		fmt.Fprint(out, "Number (q to quit): ")
		line, err := in.ReadString('\n')
		line = strings.TrimSpace(line)
		if strings.EqualFold(line, "q") || line == "" && err != nil {
			return "", errors.New("login: cancelled")
		}
		var n int
		if _, serr := fmt.Sscanf(line, "%d", &n); serr == nil && n >= 1 && n <= len(choices) {
			pick = &choices[n-1]
		} else if serr == nil && n > len(choices) && n <= len(choices)+len(extras) {
			return chooseExtra(n - len(choices) - 1)
		}
	}
	fmt.Fprintf(out, "Paste your %s key (hidden; kept in %s, readable by you only): ", pick.name, tildePath(config.AuthPath(userHome())))
	key, err := secret()
	if err != nil || key == "" {
		return "", errors.New("login: no key entered")
	}
	if err := config.SaveStoredKey(userHome(), pick.env, key); err != nil {
		return "", fmt.Errorf("login: %w", err)
	}
	harden.Provide(pick.env, key)
	fmt.Fprintf(out, "Saved. (%s in the environment still takes precedence over it.)\n", pick.env)
	return pick.name, nil
}

// signInChatGPT signs the person in with the browser (OpenAI's "Sign in with ChatGPT"). Where the browser cannot be opened the address it
// would end on can be pasted instead.
func signInChatGPT(ctx context.Context, in *bufio.Reader, out io.Writer) error {
	st, err := chatgptauth.Login(ctx, chatgptauth.Options{Path: chatgptauth.Path(userHome())}, chatgptauth.LoginIO{
		Out: out, Open: chatgptauth.OpenBrowser, Paste: func() (string, error) { return in.ReadString('\n') },
	})
	if err != nil {
		return fmt.Errorf("login: %w", err)
	}
	fmt.Fprintf(out, "Signed in as %s. Your ChatGPT plan pays for the requests (its usage limits apply); `sleipnir logout chatgpt` ends the sign-in.\n", st.Who())
	return nil
}

func joinNames(cs []loginChoice) string {
	names := make([]string, len(cs))
	for i, c := range cs {
		names[i] = c.name
	}
	return strings.Join(names, ", ")
}

func userHome() string {
	h, _ := os.UserHomeDir()
	return h
}

// cmdLogin stores a provider's API key: sleipnir login [provider]. With a pipe, the key is read from the first line of stdin.
func cmdLogin(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("login", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, "usage: sleipnir login [provider]\n\nAsks which provider (Heimdall is the recommended one) and for its API key, and keeps the key in\n~/.sleipnir/auth.json, readable by you only. `sleipnir login chatgpt` signs in with your ChatGPT plan instead (a browser, no key). From a pipe the key is the first line of stdin:\n  echo \"$KEY\" | sleipnir login heimdall\nAn environment variable of the key's usual name (HEIMDALL_API_KEY) still takes precedence.\n")
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, _, err := config.Load(config.LoadOpts{UntrustedProject: true})
	if err != nil {
		return err
	}
	name := ""
	if fs.NArg() > 0 {
		name = fs.Arg(0)
	}
	in := bufio.NewReader(os.Stdin)
	picked, err := login(ctx, in, os.Stderr, func() (string, error) { return readSecret(in) }, cfg, name, nil)
	if err != nil {
		return err
	}
	if picked == chatgptName {
		return nil // a sign-in, not a key: there is nothing to try
	}
	// Try the key on the provider's first chat model: the catalogue is often public and says nothing about the key.
	base, _, _ := session.ProviderInfo(cfg, picked)
	rows, _ := fetchModels(ctx, []modelSource{{name: picked, base: base}})
	for _, r := range rows {
		if r.SupportsTools() {
			return checkKey(ctx, cfg, r.Ref, os.Stderr)
		}
	}
	return nil
}

// cmdLogout removes a stored key: sleipnir logout <provider>.
func cmdLogout(ctx context.Context, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: sleipnir logout <provider>")
	}
	if strings.EqualFold(args[0], chatgptName) {
		st, err := chatgptauth.Open(chatgptauth.Options{Path: chatgptauth.Path(userHome())})
		if err != nil {
			return fmt.Errorf("logout: %w", err)
		}
		if err := st.Logout(ctx); err != nil {
			fmt.Fprintln(os.Stderr, "logout:", err) // signed out here all the same
			return nil
		}
		fmt.Fprintln(os.Stderr, "signed out of ChatGPT, and the issuer was told to revoke the token")
		return nil
	}
	cfg, _, err := config.Load(config.LoadOpts{UntrustedProject: true})
	if err != nil {
		return err
	}
	_, env, ok := session.ProviderInfo(cfg, strings.ToLower(args[0]))
	if !ok || env == "" {
		return fmt.Errorf("logout: %q is not a provider that takes a key", args[0])
	}
	if err := config.SaveStoredKey(userHome(), env, ""); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "removed the stored key for %s (a %s in the environment is untouched)\n", args[0], env)
	return nil
}
