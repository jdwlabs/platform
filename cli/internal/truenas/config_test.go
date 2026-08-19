package truenas

import "testing"

// The middleware's TLS port is a default, not an override: an operator pointing
// PLATFORMCTL_TRUENAS_ADDR at a NAS behind a different port means that port.
func TestDriverConfig_EndpointKeepsAPortTheHostAlreadyCarries(t *testing.T) {
	for _, tc := range []struct{ host, want string }{
		{"nas.lan", "nas.lan:443"},
		{"nas.lan:8443", "nas.lan:8443"},
		{"192.168.1.205", "192.168.1.205:443"},
		{"2001:db8::1", "[2001:db8::1]:443"},
		{"[2001:db8::1]:8443", "[2001:db8::1]:8443"},
	} {
		cfg := NewDriverConfigForTest(ClassISCSI, tc.host, "storage/k8s/iscsi/vols", "unused")
		if got := cfg.Endpoint(); got != tc.want {
			t.Errorf("Endpoint() for host %q = %q, want %q", tc.host, got, tc.want)
		}
	}
}
