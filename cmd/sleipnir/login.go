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

// login asks which provider (unless named), reads its key, and stores it in ~/.sleipnir/auth.json and in this process. It returns the
// provider's name.
func login(in *bufio.Reader, out io.Writer, secret func() (string, error), cfg *config.Config, name string, local []modelSource) (string, error) {
	choices := loginChoices(cfg)
	var pick *loginChoice
	for i := range choices {
		if choices[i].name == strings.ToLower(name) {
			pick = &choices[i]
		}
	}
	if name != "" && pick == nil {
		return "", fmt.Errorf("login: %q is not a provider that takes a key (those that do: %s; a local server needs none)", name, joinNames(choices))
	}
	if pick == nil && arrowOK() {
		var labels []string
		for _, c := range choices {
			if c.name == "heimdall" {
				c.name += "  (recommended)"
			}
			labels = append(labels, c.name)
		}
		for _, l := range local {
			labels = append(labels, l.name+"  (running on this machine, no key)")
		}
		i, err := selectRows(in, out, "Which provider will you use?", labels, labels, false, pickRows)
		if err != nil {
			return "", errors.New("login: cancelled")
		}
		if i >= len(choices) {
			return local[i-len(choices)].name, nil
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
		for i, l := range local { // a server running on this machine needs no key
			fmt.Fprintf(out, "  %2d. %s  (running on this machine, no key)\n", len(choices)+i+1, l.name)
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
		} else if serr == nil && n > len(choices) && n <= len(choices)+len(local) {
			return local[n-len(choices)-1].name, nil
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
		fmt.Fprint(os.Stderr, "usage: sleipnir login [provider]\n\nAsks which provider (Heimdall is the recommended one) and for its API key, and keeps the key in\n~/.sleipnir/auth.json, readable by you only. From a pipe the key is the first line of stdin:\n  echo \"$KEY\" | sleipnir login heimdall\nAn environment variable of the key's usual name (HEIMDALL_API_KEY) still takes precedence.\n")
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
	picked, err := login(in, os.Stderr, func() (string, error) { return readSecret(in) }, cfg, name, nil)
	if err != nil {
		return err
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
func cmdLogout(_ context.Context, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: sleipnir logout <provider>")
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
