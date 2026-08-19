package tenants

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidate(t *testing.T) {
	tests := []struct {
		name      string
		path      string
		wantErr   bool
		errSubstr string
	}{
		{"valid", "valid-tenant.yaml", false, ""},
		{"missing name", "missing-name.yaml", true, "missing field: name"},
		{"service missing fields", "service-missing-fields.yaml", true, "missing fields"},
		{"service no chart", "service-no-chart.yaml", true, `must have either "chart" or "chartPath"`},
		{"observability valid", "observability-valid.yaml", false, ""},
		{"observability bad access", "observability-bad-access.yaml", true, `access must be "view" or "edit"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateFile(filepath.Join("testdata", tc.path))
			if tc.wantErr && err == nil {
				t.Fatalf("want error containing %q, got nil", tc.errSubstr)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected: %v", err)
			}
			if tc.wantErr && err != nil {
				if !contains(err.Error(), tc.errSubstr) {
					t.Fatalf("err %q missing substring %q", err.Error(), tc.errSubstr)
				}
			}
		})
	}
}

// Two misspellings of gitSyncFolder in one block. Each one silently drops the
// key and leaves that tenant's synced folder readable by every Grafana user,
// and reporting only the first costs an edit-and-rerun cycle to find the other.
func TestValidate_ReportsEveryUnknownKeyAtOnce(t *testing.T) {
	err := ValidateFile(filepath.Join("testdata", "observability-unknown-keys.yaml"))
	if err == nil {
		t.Fatal("want an error for two unmodelled keys, got nil")
	}
	for _, key := range []string{"observability.grafana.gitsyncFolder", "observability.grafana.gitSyncFoldr"} {
		if !contains(err.Error(), key) {
			t.Errorf("err %q does not name %s", err.Error(), key)
		}
	}
	if !contains(err.Error(), "gitSyncFolder") {
		t.Errorf("err %q must list the valid names to correct them to", err.Error())
	}
}

func TestValidateDir_AllPass(t *testing.T) {
	tmp := t.TempDir()
	raw, err := os.ReadFile(filepath.Join("testdata", "valid-tenant.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	tenantDir := filepath.Join(tmp, "demo")
	if err := os.MkdirAll(tenantDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tenantDir, "tenant.yaml"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ValidateDir(tmp); err != nil {
		t.Fatalf("ValidateDir: %v", err)
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
