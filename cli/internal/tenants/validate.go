package tenants

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
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
	if err := checkObservabilityKeys(path, raw); err != nil {
		return err
	}
	return validateTenant(path, &t)
}

// checkObservabilityKeys rejects a key the observability schema does not model.
// The decoder is not strict, so a misspelled key is dropped in silence, and
// every key in this block sets a tenant boundary: a swallowed gitSyncFolder
// leaves that tenant's synced Grafana folder readable by every user in the
// instance, and looks identical to a tenant that legitimately has none.
//
// Scoped to this block rather than the whole file on purpose — most of
// tenant.yaml is consumed by the chart directly and is not modelled here at
// all, so whole-file strictness would reject keys that are correct.
func checkObservabilityKeys(path string, raw []byte) error {
	var doc struct {
		Observability map[string]interface{} `json:"observability"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("parsing %s: %w", path, err)
	}
	if err := rejectUnknownKeys(path, "observability", doc.Observability, Observability{}); err != nil {
		return err
	}
	grafana, _ := doc.Observability["grafana"].(map[string]interface{})
	return rejectUnknownKeys(path, "observability.grafana", grafana, ObsGrafanaBlock{})
}

func rejectUnknownKeys(path, prefix string, got map[string]interface{}, schema interface{}) error {
	known := modelledKeys(schema)
	for key := range got {
		if known[key] {
			continue
		}
		valid := make([]string, 0, len(known))
		for name := range known {
			valid = append(valid, name)
		}
		sort.Strings(valid)
		return fmt.Errorf("%s: unknown field %s.%s; valid: %s", path, prefix, key, strings.Join(valid, ", "))
	}
	return nil
}

// modelledKeys reads the json tags the YAML decoder actually matches on, so a
// field added to the schema is accepted here without a second list to update.
func modelledKeys(schema interface{}) map[string]bool {
	out := map[string]bool{}
	rt := reflect.TypeOf(schema)
	for i := 0; i < rt.NumField(); i++ {
		name, _, _ := strings.Cut(rt.Field(i).Tag.Get("json"), ",")
		if name != "" && name != "-" {
			out[name] = true
		}
	}
	return out
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
		if svc.Namespace == "" {
			missing = append(missing, "namespace")
		}
		if svc.PostInstall == nil {
			missing = append(missing, "postInstall")
		}
		if svc.SyncWave == nil {
			missing = append(missing, "syncWave")
		}
		// rawManifests services don't use chart/repo/revision
		if !svc.RawManifests {
			if svc.Repo == "" {
				missing = append(missing, "repo")
			}
			if svc.Revision == "" {
				missing = append(missing, "revision")
			}
		}
		if len(missing) > 0 {
			return fmt.Errorf("%s: service %s missing fields: %s", path, svc.Name, strings.Join(missing, ","))
		}
		if !svc.RawManifests && svc.Chart == "" && svc.ChartPath == "" {
			return fmt.Errorf(`%s: service %s must have either "chart" or "chartPath"`, path, svc.Name)
		}
	}
	if t.Observability != nil {
		var missing []string
		if t.Observability.TenantID == "" {
			missing = append(missing, "observability.tenantId")
		}
		if t.Observability.Grafana.Folder == "" {
			missing = append(missing, "observability.grafana.folder")
		}
		if t.Observability.Grafana.Team == "" {
			missing = append(missing, "observability.grafana.team")
		}
		switch t.Observability.Grafana.Access {
		case "view", "edit":
			// valid
		default:
			return fmt.Errorf(`%s: observability.grafana.access must be "view" or "edit", got %q`,
				path, t.Observability.Grafana.Access)
		}
		if len(missing) > 0 {
			return fmt.Errorf("%s: observability missing fields: %s", path, strings.Join(missing, ","))
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
