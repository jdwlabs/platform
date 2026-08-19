package truenas

import (
	"context"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"
)

// The middleware's TLS port is a default, not an override: an operator pointing
// PLATFORMCTL_TRUENAS_ADDR at a NAS behind a different port means that port.
func TestDriverConfig_EndpointKeepsAPortTheHostAlreadyCarries(t *testing.T) {
	for _, tc := range []struct{ host, want string }{
		{"nas.lan", "nas.lan:443"},
		{"nas.lan:8443", "nas.lan:8443"},
		{"192.168.1.205", "192.168.1.205:443"},
		{"2001:db8::1", "[2001:db8::1]:443"},
		{"[2001:db8::1]:8443", "[2001:db8::1]:8443"},
		// The bracketed literal with no port is what an operator copies out of
		// a URL, and a trailing colon is not a port. Both used to reach the
		// dialer as an address nothing answers on.
		{"[2001:db8::1]", "[2001:db8::1]:443"},
		{"nas.lan:", "nas.lan:443"},
		{"[2001:db8::1]:", "[2001:db8::1]:443"},
	} {
		cfg := NewDriverConfigForTest(ClassISCSI, tc.host, "storage/k8s/iscsi/vols", "unused")
		if got := cfg.Endpoint(); got != tc.want {
			t.Errorf("Endpoint() for host %q = %q, want %q", tc.host, got, tc.want)
		}
	}
}

// An empty parent makes every dataset on the pool a candidate. A single segment
// is the same mistake one level down, and it is the value a hand-edited config
// most plausibly carries.
func TestParseDriverConfig_RefusesADatasetParentThatIsAPoolRoot(t *testing.T) {
	for _, parent := range []string{"", "/", "storage", "/storage/"} {
		raw := []byte("httpConnection:\n  host: nas.lan\n  apiKey: k\nzfs:\n  datasetParentName: " + parent + "\n")
		if _, err := ParseDriverConfig(ClassNFS, ProvisionerNFS, raw); err == nil {
			t.Errorf("datasetParentName %q was accepted", parent)
		}
	}
	raw := []byte("httpConnection:\n  host: nas.lan\n  apiKey: k\nzfs:\n  datasetParentName: storage/k8s/vols\n")
	if _, err := ParseDriverConfig(ClassNFS, ProvisionerNFS, raw); err != nil {
		t.Errorf("a real parent was refused: %v", err)
	}
}

func credentialFixture() *k8sfake.Clientset {
	return k8sfake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: nfsConfigSecret, Namespace: DriverNamespace},
		Data: map[string][]byte{configSecretKey: []byte(
			"httpConnection:\n  host: nas.lan\n  apiKey: the-csi-key\nzfs:\n  datasetParentName: storage/k8s/vols\n")},
	})
}

// An authentication attempt against the democratic-csi key is a mutation, and
// a rejected one erodes the credential every provision, delete, expansion and
// snapshot on both classes depends on. Reaching for it has to be a decision.
func TestLoadDriverConfigs_RefusesTheCSIKeyUntilItIsOptedInto(t *testing.T) {
	ctx := context.Background()

	_, err := LoadDriverConfigs(ctx, credentialFixture(), []string{ClassNFS}, false)
	if !errors.Is(err, ErrNoAPIKey) {
		t.Fatalf("err = %v, want ErrNoAPIKey", err)
	}
	if strings.Contains(err.Error(), "the-csi-key") {
		t.Errorf("the refusal leaked the credential: %v", err)
	}

	cfgs, err := LoadDriverConfigs(ctx, credentialFixture(), []string{ClassNFS}, true)
	if err != nil {
		t.Fatalf("the opt-in must be honoured: %v", err)
	}
	if cfgs[0].APIKey() != "the-csi-key" {
		t.Errorf("the opt-in must use the driver config's key, got %q", cfgs[0].APIKey())
	}
}

func TestLoadDriverConfigs_EnvKeyIsTheOrdinaryPathAndOverridesTheCSIKey(t *testing.T) {
	t.Setenv(APIKeyEnv, "throwaway")

	cfgs, err := LoadDriverConfigs(context.Background(), credentialFixture(), []string{ClassNFS}, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfgs[0].APIKey() != "throwaway" {
		t.Errorf("APIKey() = %q, want the environment's key", cfgs[0].APIKey())
	}
}

// The stray-target scan is the only thing that finds a target a failed plan
// left mapped to no extent, so whether it can run is a property the report has
// to be able to state.
func TestDriverConfig_StrayTargetDetectionNeedsBothAffixes(t *testing.T) {
	cfg := iscsiConfig()
	if !cfg.DetectsStrayTargets() {
		t.Errorf("both affixes are set, so the scan must run")
	}
	cfg.TargetSuffix = ""
	if cfg.DetectsStrayTargets() {
		t.Errorf("a missing suffix must disable the scan rather than widen it")
	}
	if nfsConfig().DetectsStrayTargets() {
		t.Errorf("the NFS class has no targets to scan")
	}
}

// The opt-in says which credential to use, not that one exists. A driver-config
// Secret that rendered with an empty apiKey would otherwise be dialled with,
// sending "" — an authentication attempt like any other.
func TestLoadDriverConfigs_RefusesAnEmptyResolvedCredential(t *testing.T) {
	kube := k8sfake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: nfsConfigSecret, Namespace: DriverNamespace},
		Data: map[string][]byte{configSecretKey: []byte(
			"httpConnection:\n  host: nas.lan\n  apiKey: \"\"\nzfs:\n  datasetParentName: storage/k8s/vols\n")},
	})

	if _, err := LoadDriverConfigs(context.Background(), kube, []string{ClassNFS}, true); !errors.Is(err, ErrNoAPIKey) {
		t.Fatalf("err = %v, want ErrNoAPIKey even with the opt-in", err)
	}
}
