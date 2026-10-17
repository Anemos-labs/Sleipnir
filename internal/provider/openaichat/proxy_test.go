package openaichat

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/provider"
	"github.com/anemos-labs/sleipnir/internal/provider/providertest"
)

// A proxy that refuses the endpoint with 403 is, through this adapter's own transport, a failure that is not retried and that
// names the host and the status, instead of Go's bare "Forbidden" retried as a network blip.
func TestTheTransportClassifiesARefusedProxyConnect(t *testing.T) {
	err := providertest.PostThroughRefusingProxy(t, newTransport(), http.StatusForbidden)
	if err == nil {
		t.Fatal("the request went through a proxy that refuses every CONNECT")
	}
	var e *provider.Error = transportError(context.Background(), err)
	if e.Retryable() {
		t.Fatalf("a refused CONNECT is retried: %v", e)
	}
	if !strings.Contains(e.Message, "api.example.test:443") || !strings.Contains(e.Message, "403 Forbidden") {
		t.Fatalf("the refusal does not name the host and the status: %q", e.Message)
	}
}
