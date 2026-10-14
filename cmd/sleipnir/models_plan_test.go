package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/chatgptauth"
	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/cost"
)

type modelCatalogTransport func(*http.Request) (*http.Response, error)

func (f modelCatalogTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// interceptModelCatalog replaces network access for the command and restores it after the test.
func interceptModelCatalog(t *testing.T, f modelCatalogTransport) {
	t.Helper()
	old := http.DefaultTransport
	http.DefaultTransport = f
	t.Cleanup(func() { http.DefaultTransport = old })
}

func saveModelCatalogSignIn(t *testing.T, home string) {
	t.Helper()
	path := chatgptauth.Path(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	connection := fmt.Sprintf(`{"version":1,"host_id":"urn:uuid:fixture","client_id":"fixture","access_token":"fixture-token","expires_at_ms":%d,"refresh_token":"fixture-refresh"}`, time.Now().Add(time.Hour).UnixMilli())
	if err := os.WriteFile(path, []byte(connection), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestExplicitPlanModelCatalogUsesSignIn(t *testing.T) {
	for _, tc := range []struct {
		name, provider string
		entry          *config.Provider
	}{
		{name: "builtin", provider: "chatgpt"},
		{name: "tuned builtin", provider: "chatgpt", entry: &config.Provider{Options: map[string]any{"context_window": 131072}}},
		{name: "alias", provider: "plan-alias", entry: &config.Provider{BaseURL: chatgptauth.Resource, Dialect: config.DialectOpenAIResponses, Auth: config.AuthChatGPTPlan}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, home := projectDir(t)
			t.Setenv("CHATGPT_BASE_URL", "")
			saveModelCatalogSignIn(t, home)
			if tc.entry != nil {
				if err := config.Save(config.UserConfigPath(home), map[string]any{"providers": map[string]config.Provider{tc.provider: *tc.entry}}); err != nil {
					t.Fatal(err)
				}
			}
			calls := 0
			interceptModelCatalog(t, func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != http.MethodGet || r.URL.String() != chatgptauth.Resource+"/models" || r.Header.Get("Authorization") != "Bearer fixture-token" {
					return nil, fmt.Errorf("catalog request used the wrong endpoint or authentication mode")
				}
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"models":[{"slug":"gpt-fixture","display_name":"Fixture","visibility":"list","context_window":131072},{"slug":"gpt-6-luna","visibility":"list","context_window":272000},{"slug":"hidden-fixture","visibility":"hide"}]}`))}, nil
			})
			var err error
			out := capture(t, func() { err = cmdModels(context.Background(), []string{"--provider", tc.provider}) })
			if err != nil {
				t.Fatal(err)
			}
			if calls != 1 || !strings.Contains(out, tc.provider+"/gpt-fixture") || strings.Contains(out, "hidden-fixture") || !strings.Contains(out, "131k") {
				t.Fatalf("calls=%d; catalog:\n%s", calls, out)
			}
			// a model the built-in table knows is listed at the API's list price of the same model, tagged as the plan's
			var luna string
			for _, line := range strings.Split(out, "\n") {
				if strings.Contains(line, tc.provider+"/gpt-6-luna") {
					luna = line
				}
			}
			if !strings.Contains(luna, "0.1000") || !strings.Contains(luna, "0.0100") || !strings.Contains(luna, "0.5000 plan") {
				t.Fatalf("gpt-6-luna is not at its list price:\n%s", out)
			}
		})
	}
}

func TestExplicitPlanModelCatalogRequiresSignInBeforeNetwork(t *testing.T) {
	projectDir(t)
	t.Setenv("CHATGPT_BASE_URL", "")
	interceptModelCatalog(t, func(r *http.Request) (*http.Response, error) {
		t.Error("unsigned plan catalog reached the network")
		return nil, errors.New("unexpected network request")
	})
	if err := cmdModels(context.Background(), []string{"--provider", "chatgpt"}); !errors.Is(err, chatgptauth.ErrNotConnected) {
		t.Fatalf("expected sign-in guidance, got %v", err)
	}
}

func TestExplicitModelCatalogEndpointDoesNotBorrowPlanCredentials(t *testing.T) {
	_, home := projectDir(t)
	saveModelCatalogSignIn(t, home)
	calls := 0
	interceptModelCatalog(t, func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != http.MethodGet || r.URL.String() != "https://catalog.example/v1/models" || r.Header.Get("Authorization") != "" {
			return nil, errors.New("bare catalog endpoint used unexpected credentials or URL")
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"data":[{"id":"local-fixture"}]}`))}, nil
	})
	var err error
	out := capture(t, func() {
		err = cmdModels(context.Background(), []string{"--provider", "chatgpt", "--base-url", "https://catalog.example/v1"})
	})
	if err != nil || calls != 1 || !strings.Contains(out, "local-fixture") || strings.Contains(out, "chatgpt/") {
		t.Fatalf("calls=%d; error=%v; catalog:\n%s", calls, err, out)
	}
}

// planRow is a ChatGPT plan's model as the catalogue lists it: nothing billed per token, and list the API's list price of the same model
// (zero when the table does not know it).
func planRow(ref string, ctx int, list cost.Price, supported ...string) modelRow {
	r := row(ref, ctx, 0, supported...)
	r.Model.Provider, r.Model.Plan, r.Model.ListPrice = planProvider, true, list
	return r
}

func TestModelCatalogTableDistinguishesPlanFromTokenPrices(t *testing.T) {
	plan := planRow("chatgpt/gpt-6-luna", 272_000, cost.Price{InputPerM: 0.1, CacheReadPerM: 0.01, OutputPerM: 0.5}, "tools", "reasoning_effort")
	unknown := planRow("chatgpt/gpt-fixture", 131072, cost.Price{}, "tools", "reasoning_effort")
	priced := row("api/priced", 32768, 2.5, "tools")
	priced.Model.Price = cost.Price{InputPerM: 1, CacheReadPerM: 0.1, OutputPerM: 2.5}
	for _, width := range []int{0, 110, 80, 60} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			var out bytes.Buffer
			if err := printModelsWidth(&out, []modelRow{plan, unknown, priced}, modelFilter{}, nil, width); err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSpace(out.String()), "\n")
			// the plan's model at the API list price, tagged; one the table does not know says so instead of $0; a per-token price untagged
			if len(lines) != 4 || !strings.Contains(lines[1], "2.5000") || strings.Contains(lines[1], "plan") ||
				!strings.Contains(lines[2], "0.5000 plan") || !strings.Contains(lines[3], "? plan") || strings.Contains(lines[3], "0.0000") {
				t.Fatalf("plan and per-token prices are ambiguous:\n%s", out.String())
			}
			if width == 0 && (!strings.Contains(lines[2], "0.1000") || !strings.Contains(lines[2], "0.0100") || !strings.Contains(lines[1], "1.0000") || !strings.Contains(lines[1], "0.1000")) {
				t.Fatalf("full price columns changed:\n%s", out.String())
			}
			for _, line := range lines {
				if width > 0 && len(line) >= width {
					t.Errorf("line exceeds %d columns: %q", width, line)
				}
			}
		})
	}
}

func TestModelPriceFilterExcludesUnpricedPlans(t *testing.T) {
	plan := planRow("chatgpt/gpt-fixture", 131072, cost.Price{}, "tools")
	listed := planRow("chatgpt/gpt-6-sol", 272_000, cost.Price{InputPerM: 2, CacheReadPerM: 0.2, OutputPerM: 10}, "tools")
	local := row("local/fixture", 8192, 0, "tools")
	priced := row("api/priced", 32768, 2.5, "tools")
	for _, tc := range []struct {
		limit                                       float64
		wantPlan, wantListed, wantLocal, wantPriced bool
	}{
		{0, true, true, true, true},
		{1, false, false, true, false},
		{2.5, false, false, true, true},
		{10, false, true, true, true}, // a plan's model is filtered by the API list price of the same model
	} {
		f := modelFilter{MaxOut: tc.limit}
		if f.keep(plan, nil) != tc.wantPlan || f.keep(listed, nil) != tc.wantListed || f.keep(local, nil) != tc.wantLocal || f.keep(priced, nil) != tc.wantPriced {
			t.Errorf("limit %g: plan=%v, listed=%v, local=%v, priced=%v", tc.limit, f.keep(plan, nil), f.keep(listed, nil), f.keep(local, nil), f.keep(priced, nil))
		}
	}
	if got := priceOut(listed); got != "$10/M out, plan" {
		t.Errorf("the /model menu says %q", got)
	}
	if got := priceOut(plan); got != "plan" {
		t.Errorf("the /model menu says %q for a plan's model without a list price", got)
	}
}
