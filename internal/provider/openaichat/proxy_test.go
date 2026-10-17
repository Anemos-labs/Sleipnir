package openaichat

import "testing"

// The transport hands a proxy's refused CONNECT to provider.CheckProxyConnect, so the refusal is classified (and not retried)
// instead of arriving as Go's bare status text.
func TestTheTransportClassifiesARefusedProxyConnect(t *testing.T) {
	if newTransport().OnProxyConnectResponse == nil {
		t.Fatal("the transport has no OnProxyConnectResponse hook")
	}
}
