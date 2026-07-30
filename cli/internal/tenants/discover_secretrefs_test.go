package tenants

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/jdwlabs/platform/internal/vault"
)

// writeRepo lays out a tenants/ tree: one tenant.yaml naming the enabled
// services, plus one ExternalSecret manifest per service directory.
func writeRepo(t *testing.T, tenant string, enabled []string, manifests map[string]string) string {
	t.Helper()
	root := t.TempDir()
	tenantDir := filepath.Join(root, tenant)
	if err := os.MkdirAll(tenantDir, 0o755); err != nil {
		t.Fatal(err)
	}

	doc := "name: " + tenant + "\nnamespaces:\n  - name: " + tenant + "\nservices:\n"
	for _, svc := range enabled {
		doc += "  - name: " + svc + "\n    chart: x\n    repo: https://example.invalid\n" +
			"    revision: 1.0.0\n    namespace: " + tenant + "\n    postInstall: true\n    syncWave: 1\n"
	}
	if len(enabled) == 0 {
		doc = "name: " + tenant + "\nnamespaces:\n  - name: " + tenant + "\nservices: []\n"
	}
	if err := os.WriteFile(filepath.Join(tenantDir, "tenant.yaml"), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}

	for svc, body := range manifests {
		dir := filepath.Join(tenantDir, "services", svc, "postInstall")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "externalsecret.yaml"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func esManifest(name, store string, refs [][2]string) string {
	doc := "apiVersion: external-secrets.io/v1\nkind: ExternalSecret\nmetadata:\n  name: " + name +
		"\n  namespace: platform\nspec:\n  secretStoreRef:\n    kind: ClusterSecretStore\n    name: " + store +
		"\n  data:\n"
	for _, r := range refs {
		doc += "    - secretKey: " + r[1] + "\n      remoteRef:\n        key: " + r[0] +
			"\n        property: " + r[1] + "\n"
	}
	return doc
}

const esDataFrom = `apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata:
  name: generated
  namespace: platform
spec:
  secretStoreRef:
    kind: ClusterSecretStore
    name: vault
  dataFrom:
    - extract:
        key: bulk-path
`

const esEmptyKey = `apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata:
  name: broken
  namespace: platform
spec:
  secretStoreRef:
    kind: ClusterSecretStore
    name: vault
  data:
    - secretKey: orphan
      remoteRef:
        property: orphan
`

// fixtureRepo returns a tree holding 8 references across every scan class:
// 2 resolvable, 3 behind a service absent from tenant.yaml, 1 on a non-vault
// store, 1 dataFrom pattern, 1 data entry with no remoteRef.key.
func fixtureRepo(t *testing.T) string {
	t.Helper()
	return writeRepo(t, "platform",
		[]string{"monitoring", "github", "generated", "broken"},
		map[string]string{
			"monitoring": esManifest("monitoring", "vault", [][2]string{
				{"holmes", "litellm_key"},
				{"holmes", "webhook_token"},
			}),
			"dormant": esManifest("dormant", "vault", [][2]string{
				{"github-app", "app_id"},
				{"github-app", "installation_id"},
				{"github-app", "private_key"},
			}),
			"github":    esManifest("github", "github-app-token", [][2]string{{"ignored", "token"}}),
			"generated": esDataFrom,
			"broken":    esEmptyKey,
		})
}

func TestDiscoverSecretRefs_AccountsForEveryRef(t *testing.T) {
	d, err := DiscoverSecretRefs(fixtureRepo(t), "vault")
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if d.Found() != 8 {
		t.Errorf("Found() = %d, want 8", d.Found())
	}
	if len(d.Refs) != 2 {
		t.Errorf("scannable refs = %d, want 2: %+v", len(d.Refs), d.Refs)
	}
	if len(d.Skipped) != 6 {
		t.Errorf("skipped = %d, want 6: %+v", len(d.Skipped), d.Skipped)
	}
	want := map[SkipReason]int{
		SkipServiceNotEnabled: 3,
		SkipStoreNotVault:     1,
		SkipDataFromPattern:   1,
		SkipNoRemoteKey:       1,
	}
	got := d.SkipCounts()
	for reason, n := range want {
		if got[reason] != n {
			t.Errorf("skip %q = %d, want %d", reason, got[reason], n)
		}
	}
	if len(got) != len(want) {
		t.Errorf("skip classes = %v, want exactly %v", got, want)
	}
}

func TestDiscoverSecretRefs_NamesSkippedService(t *testing.T) {
	d, err := DiscoverSecretRefs(fixtureRepo(t), "vault")
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	for _, s := range d.Skipped {
		if s.Reason != SkipServiceNotEnabled {
			continue
		}
		if s.Service != "dormant" {
			t.Errorf("skipped service = %q, want dormant", s.Service)
		}
		if s.File == "" {
			t.Error("skipped ref carries no file path")
		}
		return
	}
	t.Fatal("no service-not-enabled skip recorded")
}

// A ref that exists only in the tree — never applied to the cluster — must
// still be resolved. This is the case a live-state scan cannot see at all.
func TestVerifyRepoSecrets_UnappliedRefFailsVerification(t *testing.T) {
	srv := mockVaultKV(t, "kv", map[string]map[string]any{
		"holmes": {"litellm_key": "x"}, // webhook_token absent
	})
	vc, err := vault.NewClient(srv.URL, "root")
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	r, err := VerifyRepoSecrets(context.Background(), vc, fixtureRepo(t), "kv", "vault")
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if r.Found != 8 {
		t.Errorf("Found = %d, want 8", r.Found)
	}
	if r.Checked != 2 {
		t.Errorf("Checked = %d, want 2", r.Checked)
	}
	if len(r.Issues) != 1 {
		t.Fatalf("issues = %d, want 1: %+v", len(r.Issues), r.Issues)
	}
	if r.Issues[0].Kind != "property-missing" || r.Issues[0].Property != "webhook_token" {
		t.Errorf("issue = %+v, want property-missing on webhook_token", r.Issues[0])
	}
	if r.Issues[0].File == "" {
		t.Error("issue carries no file path")
	}
}

// Skipped refs must never produce an issue: a dormant service's Vault path is
// legitimately unseeded, and failing on it would make the gate unusable.
func TestVerifyRepoSecrets_SkippedRefsAreNotResolved(t *testing.T) {
	srv := mockVaultKV(t, "kv", map[string]map[string]any{
		"holmes": {"litellm_key": "x", "webhook_token": "y"},
	})
	vc, err := vault.NewClient(srv.URL, "root")
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	r, err := VerifyRepoSecrets(context.Background(), vc, fixtureRepo(t), "kv", "vault")
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if r.HasIssues() {
		t.Fatalf("expected no issues, got: %+v", r.Issues)
	}
	if len(r.Skipped) != 6 {
		t.Errorf("skipped = %d, want 6", len(r.Skipped))
	}
}

func TestVerifyReport_SummaryNamesSkippedClasses(t *testing.T) {
	r := VerifyReport{
		Found:   8,
		Checked: 2,
		Skipped: []SkippedRef{
			{Service: "dormant", Reason: SkipServiceNotEnabled},
			{Service: "dormant", Reason: SkipServiceNotEnabled},
			{Service: "generated", Reason: SkipDataFromPattern},
		},
	}
	got := r.Summary()
	want := "8 refs found; 2 checked; 3 not checked (datafrom-pattern=1, service-not-enabled=2); 0 issues"
	if got != want {
		t.Errorf("Summary() =\n  %q\nwant\n  %q", got, want)
	}
}

func TestVerifyReport_SkipLinesNameOwnersPerClass(t *testing.T) {
	r := VerifyReport{
		Skipped: []SkippedRef{
			{Service: "arc-runner-set-jdwlabs", Reason: SkipServiceNotEnabled},
			{Service: "arc-runner-set-jdwlabs", Reason: SkipServiceNotEnabled},
			{Service: "arc-runner-set-dotablaze-tech", Reason: SkipServiceNotEnabled},
			{ESName: "generated", Reason: SkipDataFromPattern},
		},
	}
	got := r.SkipLines()
	want := []string{
		"datafrom-pattern: 1 ref(s) — generated",
		"service-not-enabled: 3 ref(s) — arc-runner-set-dotablaze-tech, arc-runner-set-jdwlabs",
	}
	if len(got) != len(want) {
		t.Fatalf("SkipLines() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("SkipLines()[%d] =\n  %q\nwant\n  %q", i, got[i], want[i])
		}
	}
}

func TestVerifyReport_SkipLinesEmptyWhenNothingSkipped(t *testing.T) {
	if lines := (VerifyReport{Found: 3, Checked: 3}).SkipLines(); len(lines) != 0 {
		t.Errorf("SkipLines() = %v, want empty", lines)
	}
}

func TestVerifyReport_SummaryIsDefinitiveWhenFullyChecked(t *testing.T) {
	r := VerifyReport{Found: 31, Checked: 31}
	want := "31 refs found; 31 checked; 0 issues"
	if got := r.Summary(); got != want {
		t.Errorf("Summary() = %q, want %q", got, want)
	}
}
