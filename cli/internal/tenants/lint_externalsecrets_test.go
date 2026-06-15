package tenants

import (
	"path/filepath"
	"testing"
)

func TestLintExternalSecrets(t *testing.T) {
	issues, err := LintExternalSecrets(filepath.Join("testdata", "externalsecrets"))
	if err != nil {
		t.Fatalf("LintExternalSecrets: %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("want 1 issue, got %d: %+v", len(issues), issues)
	}
	got := issues[0]
	if got.ESName != "bad-secret" {
		t.Errorf("ESName = %q, want bad-secret", got.ESName)
	}
	if got.SecretKey != "bare" {
		t.Errorf("SecretKey = %q, want bare", got.SecretKey)
	}
	wantMissing := []string{"conversionStrategy", "decodingStrategy", "metadataPolicy"}
	if len(got.Missing) != len(wantMissing) {
		t.Fatalf("Missing = %v, want %v", got.Missing, wantMissing)
	}
	for i, f := range wantMissing {
		if got.Missing[i] != f {
			t.Errorf("Missing[%d] = %q, want %q", i, got.Missing[i], f)
		}
	}
}

// The repository's own tenant manifests must satisfy the lint, so a missing
// remoteRef field anywhere under tenants/ fails CI.
func TestLintExternalSecrets_RepoClean(t *testing.T) {
	root := filepath.Join("..", "..", "..", "tenants")
	issues, err := LintExternalSecrets(root)
	if err != nil {
		t.Fatalf("LintExternalSecrets(%s): %v", root, err)
	}
	if len(issues) != 0 {
		for _, iss := range issues {
			t.Errorf("%s", iss.Error())
		}
		t.Fatalf("%d ExternalSecret entr(ies) under tenants/ omit required remoteRef fields", len(issues))
	}
}
