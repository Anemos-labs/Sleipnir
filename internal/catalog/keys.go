package catalog

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/anemos-labs/sleipnir/internal/chatgptauth"
	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/harden"
	"github.com/anemos-labs/sleipnir/internal/provider"
	"github.com/anemos-labs/sleipnir/internal/session"
)

// The provider-status service. A key is a secret held in this process (harden) and stored, by `sleipnir login`, in
// ~/.sleipnir/auth.json; nothing here returns, logs or formats one. What leaves is a provider's name, the name of its key's variable,
// whether a key is there and where it comes from.

// ChatGPTName is the provider that signs in with a browser and needs no key: the person's ChatGPT plan.
const ChatGPTName = "chatgpt"

// Key sources, as KeyStatus.Source reports them.
const (
	// SourceEnv: the key in use came from the environment (set at start, or since).
	SourceEnv = "env"
	// SourceStored: the key in use is the one `sleipnir login` stored.
	SourceStored = "stored"
	// SourceNone: there is no key.
	SourceNone = "none"
	// SourceUnknown: a key is there but where it came from cannot be told (the stored keys could not be read).
	SourceUnknown = "unknown"
)

// KeyProvider is a provider that takes a key: its name and the environment variable of the key.
type KeyProvider struct {
	Name, Env string
}

// KeyProviders are the providers that take a key, Heimdall first (it is the recommended one), then the others in alphabetical order.
func KeyProviders(cfg *config.Config) []KeyProvider {
	var out []KeyProvider
	for _, n := range session.ProviderNames(cfg) {
		if _, env, ok := session.ProviderInfo(cfg, n); ok && env != "" {
			out = append(out, KeyProvider{n, env})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if (out[i].Name == "heimdall") != (out[j].Name == "heimdall") {
			return out[i].Name == "heimdall"
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// KeyStatus is what may be said about a provider's key: never the key.
type KeyStatus struct {
	Provider string `json:"provider"`
	EnvName  string `json:"env_name"`
	Present  bool   `json:"present"`
	// Source is SourceEnv, SourceStored, SourceNone or SourceUnknown.
	Source string `json:"source"`
}

// StatusOf reports the key of one provider: whether this process holds a value for its variable (harden.Secret) and where that
// value comes from. The environment wins over a stored key: a value that is not the stored one came from the environment; the stored
// one is reported as stored.
func StatusOf(home string, kp KeyProvider) KeyStatus {
	st := KeyStatus{Provider: kp.Name, EnvName: kp.Env, Source: SourceNone}
	if kp.Env == "" {
		return st
	}
	v := strings.TrimSpace(harden.Secret(kp.Env))
	if v == "" {
		return st
	}
	st.Present = true
	stored, err := config.StoredKeys(home)
	switch sv, ok := stored[kp.Env]; {
	case err != nil:
		st.Source = SourceUnknown
	case ok && strings.TrimSpace(sv) == v:
		st.Source = SourceStored
	default:
		st.Source = SourceEnv
	}
	return st
}

// Statuses reports the key of every provider that takes one, in KeyProviders' order.
func Statuses(cfg *config.Config, home string) []KeyStatus {
	kps := KeyProviders(cfg)
	out := make([]KeyStatus, 0, len(kps))
	for _, kp := range kps {
		out = append(out, StatusOf(home, kp))
	}
	return out
}

// StoreKey keeps a provider's key: in auth.json under home (mode 0600, written atomically, one writer at a time in this process) and
// in this process's memory (harden.Provide), where a variable of the same name set in the environment still wins. The key is not
// checked here (CheckKey).
func StoreKey(home, env, key string) error {
	key = strings.TrimSpace(key)
	if env == "" || key == "" {
		return errors.New("a key and the name of its variable are required")
	}
	unlock := config.WriteLock(config.AuthPath(home))
	defer unlock()
	if err := config.SaveStoredKey(home, env, key); err != nil {
		return err
	}
	harden.Provide(env, key)
	return nil
}

// ForgetKey removes a provider's stored key from auth.json under home and from this process's memory. A key that came from the
// environment is not the stored one and is not removed by it.
func ForgetKey(home, env string) error {
	if env == "" {
		return errors.New("the name of the key's variable is required")
	}
	unlock := config.WriteLock(config.AuthPath(home))
	defer unlock()
	if err := config.SaveStoredKey(home, env, ""); err != nil {
		return err
	}
	harden.Provide(env, "")
	return nil
}

// ChatGPTStatus says whether a ChatGPT plan is signed in on this machine (the sign-in kept under home) and, when it is, the account's
// e-mail address or name as the sign-in page gave it. No token is read into the result.
func ChatGPTStatus(home string) (connected bool, who string) {
	path := chatgptauth.Path(home)
	if !chatgptauth.Connected(path) {
		return false, ""
	}
	st, err := chatgptauth.Open(chatgptauth.Options{Path: path})
	if err != nil {
		return false, ""
	}
	return true, st.Who()
}

// ChatGPTSignOut ends the ChatGPT sign-in kept under home: the tokens are forgotten, and the issuer is asked to revoke the refresh
// token (best effort: an error says it was not told, the sign-in is over here all the same).
func ChatGPTSignOut(ctx context.Context, home string) error {
	st, err := chatgptauth.Open(chatgptauth.Options{Path: chatgptauth.Path(home)})
	if err != nil {
		return err
	}
	return st.Logout(ctx)
}

// KeyCheck is the outcome of trying a key with one small request.
type KeyCheck struct {
	// Checked says a request was sent and answered (or refused); false when the model could not be resolved or the network failed,
	// which says nothing about the key.
	Checked bool
	// Refused says the provider refused the key itself (401, 403).
	Refused bool
	// Provider and Env name the provider and its key's variable.
	Provider, Env string
	// Reason is the provider's message on a refusal, sanitized and short.
	Reason string
}

// CheckKey sends one request of eight tokens to the model ref with the key this process holds for its provider, because a catalogue
// is often public and answers whatever the key is. Only a refusal of the key itself counts; any other failure (a slow network, a busy
// model) leaves Checked false. It changes nothing: forgetting a refused key is the caller's decision.
func CheckKey(ctx context.Context, cfg *config.Config, ref string) KeyCheck {
	mr, err := session.ResolveModel(cfg, ref)
	if err != nil {
		return KeyCheck{}
	}
	out := KeyCheck{Provider: mr.Provider}
	if _, env, ok := session.ProviderInfo(cfg, mr.Provider); ok {
		out.Env = env
	}
	p, _, err := session.BuildProvider(cfg, mr, session.ProviderOptions{})
	if err != nil {
		return out
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	prompt := &core.Prompt{Model: mr.Model, Params: core.Params{MaxTokens: 8}, Messages: []core.Message{{Role: core.RoleUser, Blocks: []core.Block{core.Text("ping")}}}}
	_, err = p.Do(ctx, &provider.Request{Prompt: prompt, Label: "login:check", NoStream: true}, nil)
	var pe *provider.Error
	switch {
	case errors.As(err, &pe) && pe.Kind == provider.ErrAuth:
		out.Checked, out.Refused = true, true
		out.Reason = provider.SanitizeText(pe.Message, 200)
	case err == nil:
		out.Checked = true
	}
	return out
}
