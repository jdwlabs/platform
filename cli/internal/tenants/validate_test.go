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
