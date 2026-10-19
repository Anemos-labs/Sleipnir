package weburl

import "testing"

// LocalHost tells a public internet host from one of this machine, the local network or a cloud metadata service, without DNS.
func TestLocalHost(t *testing.T) {
	public := []string{"example.com", "docs.example.org", "93.184.216.34", "2606:2800:220:1:248:1893:25c8:1946", "example.com.", "xn--bcher-kva.test"}
	local := []string{
		"", "localhost", "LOCALHOST", "localhost.", "app.localhost", "intranet", "printer.local", "nas.lan", "metadata.google.internal",
		"router.home.arpa", "127.0.0.1", "127.1", "2130706433", "0x7f.0.0.1", "10.0.0.5", "172.16.0.1", "192.168.1.1", "100.64.0.1",
		"169.254.169.254", "fd00:ec2::254", "[fd00:ec2::254]", "::1", "[::1]", "::ffff:127.0.0.1", "fe80::1%eth0", "0.0.0.0", "::",
		"224.0.0.1", "ff02::1", "1.2.3.4.5", "999.1.1.1",
	}
	for _, h := range public {
		if why := LocalHost(h); why != "" {
			t.Errorf("LocalHost(%q) = %q, want public", h, why)
		}
	}
	for _, h := range local {
		if LocalHost(h) == "" {
			t.Errorf("LocalHost(%q) = \"\", want a reason", h)
		}
	}
}
