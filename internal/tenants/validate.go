package tenants

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"sigs.k8s.io/yaml"
)

// ValidateFile loads a single tenant.yaml from disk and returns nil on success.
func ValidateFile(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	var t Tenant
	if err := yaml.Unmarshal(raw, &t); err != nil {
		return fmt.Errorf("parsing %s: %w", path, err)
	}
	return validateTenant(path, &t)
}

// LoadFile parses a tenant.yaml without validating.
func LoadFile(path string) (*Tenant, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var t Tenant
	if err := yaml.Unmarshal(raw, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

func validateTenant(path string, t *Tenant) error {
	if t.Name == "" {
		return fmt.Errorf("%s: missing field: name", path)
	}
	if t.Namespaces == nil {
		return fmt.Errorf("%s: missing field: namespaces", path)
	}
	if t.Services == nil {
		return fmt.Errorf("%s: missing field: services", path)
	}
	for _, svc := range t.Services {
		var missing []string
		if svc.Name == "" {
			missing = append(missing, "name")
		}
		if svc.Repo == "" {
			missing = append(missing, "repo")
		}
		if svc.Revision == "" {
			missing = append(missing, "revision")
		}
		if svc.Namespace == "" {
			missing = append(missing, "namespace")
		}
		if svc.PostInstall == nil {
			missing = append(missing, "postInstall")
		}
		if svc.SyncWave == nil {
			missing = append(missing, "syncWave")
		}
		if len(missing) > 0 {
			return fmt.Errorf("%s: service %s missing fields: %s", path, svc.Name, strings.Join(missing, ","))
		}
		if svc.Chart == "" && svc.ChartPath == "" {
			return fmt.Errorf(`%s: service %s must have either "chart" or "chartPath"`, path, svc.Name)
		}
	}
	return nil
}

// ValidateDir scans dir for files matching */tenant.yaml and returns first failure.
func ValidateDir(dir string) error {
	matches, err := filepath.Glob(filepath.Join(dir, "*", "tenant.yaml"))
	if err != nil {
		return err
	}
	if len(matches) == 0 {
		return fmt.Errorf("no tenant.yaml files found under %s", dir)
	}
	var firstErr error
	for _, m := range matches {
		if err := ValidateFile(m); err != nil {
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}
