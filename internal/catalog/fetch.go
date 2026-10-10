package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/anemos-labs/sleipnir/internal/chatgptauth"
	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/cost"
	"github.com/anemos-labs/sleipnir/internal/harden"
	"github.com/anemos-labs/sleipnir/internal/provider"
	"github.com/anemos-labs/sleipnir/internal/provider/gateway"
	"github.com/anemos-labs/sleipnir/internal/session"
)

// Source is one catalogue to ask: the provider's name ("" for a bare endpoint) and its base URL. A ChatGPT plan's list needs the
// sign-in's token and is shaped differently, so it is asked another way (PlanModels).
type Source struct {
	Name, Base string
	Plan       bool
}

// UsableProviders are the providers a listing asks when none is named: those whose key is set, a signed-in ChatGPT plan, and (with
// withPublic) Heimdall, whose catalogue is public (the chat asks only those with a key: it must not reach the network for a session
// that has none). Local servers and providers without a catalogue route are asked only by name.
func UsableProviders(cfg *config.Config, withPublic bool) []string {
	var out []string
	for _, n := range session.ProviderNames(cfg) {
		p, ok := session.LookupProvider(cfg, n)
		if ok && p.Auth == config.AuthChatGPTPlan { // a ChatGPT plan has no key: the sign-in is what makes it usable
			if session.ProviderReady(p) {
				out = append(out, n)
			}
			continue
		}
		if !ok || p.EffectiveDialect() != config.DialectOpenAIChat {
			continue
		}
		if withPublic && n == "heimdall" || p.APIKeyEnv != "" && harden.Secret(p.APIKeyEnv) != "" {
			out = append(out, n)
		}
	}
	return out
}

// UsableSources resolves the catalogue endpoints of UsableProviders.
func UsableSources(cfg *config.Config, withPublic bool) []Source {
	var out []Source
	for _, n := range UsableProviders(cfg, withPublic) {
		b, _, _ := session.ProviderInfo(cfg, n)
		p, _ := session.LookupProvider(cfg, n)
		out = append(out, Source{Name: n, Base: b, Plan: p.Auth == config.AuthChatGPTPlan})
	}
	return out
}

// LocalSources are the local servers (Ollama, LM Studio, llama.cpp, vLLM: providers with no key whose address is this machine) that
// answer with a list of models right now. A server that is not running refuses at once, so asking costs nothing; each is asked for a
// second and a half at most, all at once.
func LocalSources(ctx context.Context, cfg *config.Config) []Source {
	var cand []Source
	for _, n := range session.ProviderNames(cfg) {
		p, ok := session.LookupProvider(cfg, n)
		if !ok || p.APIKeyEnv != "" || p.EffectiveDialect() != config.DialectOpenAIChat {
			continue
		}
		base, _, _ := session.ProviderInfo(cfg, n)
		if u, err := url.Parse(base); err == nil && provider.IsLoopbackHost(u.Hostname()) {
			cand = append(cand, Source{Name: n, Base: base})
		}
	}
	up := make([]bool, len(cand))
	var wg sync.WaitGroup
	for i, c := range cand {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
			defer cancel()
			es, err := gateway.Fetch(cctx, &http.Client{Timeout: 1500 * time.Millisecond}, c.Base)
			up[i] = err == nil && len(es) > 0
		}()
	}
	wg.Wait()
	var out []Source
	for i, c := range cand {
		if up[i] {
			out = append(out, c)
		}
	}
	return out
}

// localSources finds the local servers that answer, and usableSources the providers Fetch asks (with Heimdall's public catalogue);
// tests replace them so that neither the network nor a server running on the test machine is reached.
var (
	localSources  = LocalSources
	usableSources = func(cfg *config.Config) []Source { return UsableSources(cfg, true) }
)

// FetchEntries asks the catalogues at once, each for 30 seconds at most, with no cache (what `sleipnir models` does). The entries
// are those of the catalogues that answered; errs says, per source, why one did not. home is where a ChatGPT plan's sign-in is kept.
func FetchEntries(ctx context.Context, home string, sources []Source) (all []Entry, errs []error) {
	rows := make([][]Entry, len(sources))
	errs = make([]error, len(sources))
	var wg sync.WaitGroup
	for i, src := range sources {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			var es []gateway.Entry
			var err error
			if src.Plan {
				es, err = PlanModels(cctx, home)
			} else {
				es, err = gateway.Fetch(cctx, &http.Client{Timeout: 30 * time.Second}, src.Base)
			}
			errs[i] = err
			rows[i] = entriesOf(src, es)
		}()
	}
	wg.Wait()
	for i := range sources {
		if errs[i] == nil {
			all = append(all, rows[i]...)
		}
	}
	return all, errs
}

// entriesOf names a source's catalogue entries by reference (provider/model, or the bare id for an endpoint with no name).
func entriesOf(src Source, es []gateway.Entry) []Entry {
	out := make([]Entry, 0, len(es))
	for _, e := range es {
		ref := e.Model.ID
		if src.Name != "" {
			ref = src.Name + "/" + ref
		}
		out = append(out, Entry{Ref: ref, Entry: e})
	}
	return out
}

// PlanModels lists the models available through the ChatGPT sign-in kept under home, with context windows when the plan's list
// gives them (0 otherwise). They have no prices.
func PlanModels(ctx context.Context, home string) ([]gateway.Entry, error) {
	st, err := chatgptauth.Open(chatgptauth.Options{Path: chatgptauth.Path(home)})
	if err != nil {
		return nil, err
	}
	ms, err := st.Models(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]gateway.Entry, 0, len(ms))
	for _, m := range ms {
		out = append(out, gateway.Entry{Model: cost.Model{ID: m.Slug, Provider: PlanProvider, ContextTokens: m.ContextWindow}, Modality: "text->text", Supported: []string{"tools", "reasoning_effort"}})
	}
	return out, nil
}

// CacheTTL is how long a downloaded catalogue is used before it is asked for again (the sessions' own price lookups keep the same
// files for the same time).
const CacheTTL = 6 * time.Hour

// Options say which catalogues Fetch reads and how.
type Options struct {
	// Home is the user's home directory: the ChatGPT sign-in, and (unless CacheDir is set) the state directory whose cache/ keeps the
	// downloaded catalogues.
	Home string
	// Cwd is the directory whose project configuration is read when Config is nil (as untrusted).
	Cwd string
	// Config is the configuration that names the providers; nil loads it for Home and Cwd.
	Config *config.Config
	// Refresh asks every catalogue again instead of using a copy younger than CacheTTL.
	Refresh bool
	// Offline reaches no network: only cached catalogues are read, whatever their age, and local servers and the ChatGPT plan are
	// not asked.
	Offline bool
	// CacheDir overrides where catalogues are cached (default: the state directory's cache/).
	CacheDir string
	// Client is the HTTP client for catalogues (default: one with a 30-second timeout).
	Client *http.Client
	// Now is the clock for the cache's age (default time.Now).
	Now func() time.Time
}

// Result is a catalogue read: the rows of every source that answered, why the others did not ("name: reason"), and the time of the
// oldest copy used (when the list was current).
type Result struct {
	Rows    []Row
	Entries []Entry
	Errors  []error
	AsOf    time.Time
}

// Fetch reads the catalogues of the usable providers (UsableSources with the public Heimdall), the local servers that answer, and a
// signed-in ChatGPT plan; a copy younger than CacheTTL is used unless Refresh. Rows are every model listed, chat or not (Row.Chat).
func Fetch(ctx context.Context, o Options) ([]Row, []error) {
	r := FetchResult(ctx, o)
	return r.Rows, r.Errors
}

// FetchResult is Fetch with the entries and the age of the list.
func FetchResult(ctx context.Context, o Options) Result {
	now := time.Now
	if o.Now != nil {
		now = o.Now
	}
	cfg := o.Config
	if cfg == nil {
		c, _, err := config.Load(config.LoadOpts{Home: o.Home, Cwd: o.Cwd, UntrustedProject: true})
		if err != nil {
			return Result{Errors: []error{err}, AsOf: now()}
		}
		cfg = c
	}
	sources := usableSources(cfg)
	if !o.Offline {
		sources = append(sources, localSources(ctx, cfg)...)
	}
	dir := o.CacheDir
	if dir == "" {
		dir = filepath.Join(filepath.Dir(session.TrustLedgerPath(o.Home)), "cache")
	}
	hc := o.Client
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	type got struct {
		es  []gateway.Entry
		at  time.Time
		err error
	}
	res := make([]got, len(sources))
	var wg sync.WaitGroup
	for i, src := range sources {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			if src.Plan {
				if o.Offline {
					res[i].err = errors.New("the ChatGPT plan's list is not kept offline")
					return
				}
				res[i].es, res[i].err = PlanModels(cctx, o.Home)
				res[i].at = now()
				return
			}
			res[i].es, res[i].at, res[i].err = cachedCatalog(cctx, hc, dir, src.Base, o.Refresh, o.Offline, now)
		}()
	}
	wg.Wait()
	out := Result{AsOf: now()}
	for i, src := range sources {
		if res[i].err != nil {
			name := src.Name
			if name == "" {
				name = src.Base
			}
			out.Errors = append(out.Errors, fmt.Errorf("%s: %w", name, res[i].err))
			continue
		}
		if !res[i].at.IsZero() && res[i].at.Before(out.AsOf) {
			out.AsOf = res[i].at
		}
		for _, e := range entriesOf(src, res[i].es) {
			out.Entries = append(out.Entries, e)
			out.Rows = append(out.Rows, RowOf(e))
		}
	}
	return out
}

// CachePath is where the catalogue of baseURL is kept under dir: catalog-<the first 12 hex digits of the URL's SHA-256>.json, the
// file the sessions' price lookups read and write.
func CachePath(dir, baseURL string) string {
	sum := sha256.Sum256([]byte(baseURL))
	return filepath.Join(dir, "catalog-"+hex.EncodeToString(sum[:6])+".json")
}

// cachedCatalog returns the catalogue of base: the cached copy when it is younger than CacheTTL (any age when offline) and refresh is
// not asked, else a download, which is then cached. The copy on disk passes the same checks as a download (gateway.Vet), so an edited
// file cannot bring in a price a download could not. at is when the list was current.
func cachedCatalog(ctx context.Context, hc *http.Client, dir, base string, refresh, offline bool, now func() time.Time) (es []gateway.Entry, at time.Time, err error) {
	path := CachePath(dir, base)
	if !refresh || offline {
		if st, serr := os.Stat(path); serr == nil && (offline || now().Sub(st.ModTime()) < CacheTTL) {
			if b, rerr := os.ReadFile(path); rerr == nil {
				var cached []gateway.Entry
				if json.Unmarshal(b, &cached) == nil {
					if cached = gateway.Vet(cached); len(cached) > 0 {
						return cached, st.ModTime(), nil
					}
				}
			}
		}
		if offline {
			return nil, time.Time{}, errors.New("no copy of its catalogue is kept here")
		}
	}
	es, err = gateway.Fetch(ctx, hc, base)
	if err != nil {
		return nil, time.Time{}, err
	}
	if len(es) > 0 {
		if b, merr := json.Marshal(es); merr == nil {
			unlock := config.WriteLock(path)
			if os.MkdirAll(dir, 0o700) == nil {
				_ = writeAtomic(path, b)
			}
			unlock()
		}
	}
	return es, now(), nil
}

// writeAtomic replaces path with data through a temporary file and a rename, readable by the user only.
func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".catalog-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
