package tenants

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
)

// requiredRemoteRefFields are the ExternalSecret spec.data[].remoteRef fields
// every entry must declare explicitly. The apiserver defaults them
// (Default/None/None) on the live object, and the services-appset no longer
// masks that drift via ignoreDifferences (doing so broke ServerSideApply
// list-merge so edits never converged). A manifest that omits them therefore
// renders permanently OutOfSync. See
// helm-charts/tenant-envelope/templates/services-appset.yaml.
var requiredRemoteRefFields = []string{
	"conversionStrategy",
	"decodingStrategy",
	"metadataPolicy",
}

// ExternalSecretIssue is one ExternalSecret data entry missing required
// remoteRef fields.
type ExternalSecretIssue struct {
	File      string
	ESName    string
	SecretKey string
	Missing   []string
}

func (i ExternalSecretIssue) Error() string {
	return fmt.Sprintf("%s: ExternalSecret %q data[secretKey=%s] missing remoteRef field(s): %s",
		i.File, i.ESName, i.SecretKey, strings.Join(i.Missing, ","))
}

// LintExternalSecrets walks root for ExternalSecret manifests and returns an
// issue for every spec.data[].remoteRef that omits a required field. Entries
// without a remoteRef (dataFrom / generator sources) are out of scope.
func LintExternalSecrets(root string) ([]ExternalSecretIssue, error) {
	var issues []ExternalSecretIssue
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if ext := filepath.Ext(path); ext != ".yaml" && ext != ".yml" {
			return nil
		}
		fileIssues, err := lintExternalSecretFile(path)
		if err != nil {
			return err
		}
		issues = append(issues, fileIssues...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(issues, func(i, j int) bool {
		if issues[i].File != issues[j].File {
			return issues[i].File < issues[j].File
		}
		if issues[i].ESName != issues[j].ESName {
			return issues[i].ESName < issues[j].ESName
		}
		return issues[i].SecretKey < issues[j].SecretKey
	})
	return issues, nil
}

func lintExternalSecretFile(path string) ([]ExternalSecretIssue, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	dec := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(raw), 4096)
	var issues []ExternalSecretIssue
	for {
		var obj map[string]any
		if err := dec.Decode(&obj); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			// A document that does not decode as a manifest map (templated or
			// non-k8s YAML) is not lintable here; stop scanning this file
			// rather than failing the whole run.
			break
		}
		if kind, _ := obj["kind"].(string); kind != "ExternalSecret" {
			continue
		}
		name := externalSecretName(obj)
		for _, entry := range dataEntries(obj) {
			rr, ok := entry["remoteRef"].(map[string]any)
			if !ok {
				continue
			}
			var missing []string
			for _, f := range requiredRemoteRefFields {
				if v, ok := rr[f]; !ok || v == nil || v == "" {
					missing = append(missing, f)
				}
			}
			if len(missing) > 0 {
				issues = append(issues, ExternalSecretIssue{
					File:      path,
					ESName:    name,
					SecretKey: stringField(entry, "secretKey"),
					Missing:   missing,
				})
			}
		}
	}
	return issues, nil
}

func externalSecretName(obj map[string]any) string {
	md, _ := obj["metadata"].(map[string]any)
	return stringField(md, "name")
}

func dataEntries(obj map[string]any) []map[string]any {
	spec, _ := obj["spec"].(map[string]any)
	rawData, _ := spec["data"].([]any)
	out := make([]map[string]any, 0, len(rawData))
	for _, e := range rawData {
		if m, ok := e.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func stringField(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	s, _ := m[key].(string)
	return s
}
