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

// SkipReason names a class of reference that a scan found but deliberately did
// not resolve against Vault. Callers must surface every class with its count:
// a narrowing the caller cannot see is indistinguishable from a clean pass,
// which is how a green verify came to cover fewer refs than the tree held.
type SkipReason string

const (
	// SkipServiceNotEnabled marks refs whose owning service has no entry in
	// tenant.yaml — commented out or removed. The manifest is never applied,
	// so its Vault path is legitimately allowed to be unseeded.
	SkipServiceNotEnabled SkipReason = "service-not-enabled"
	// SkipStoreNotVault marks refs bound to some other secret store, whose
	// backend this command does not read.
	SkipStoreNotVault SkipReason = "store-not-vault"
	// SkipDataFromPattern marks spec.dataFrom sources: they name a path or a
	// find-pattern rather than an explicit property, so there is no single
	// (key, property) pair to assert.
	SkipDataFromPattern SkipReason = "datafrom-pattern"
	// SkipNoRemoteKey marks a spec.data entry with no remoteRef.key, which
	// cannot address a Vault path at all.
	SkipNoRemoteKey SkipReason = "no-remote-key"
)

// SkippedRef is one reference left unresolved, with the reason why.
type SkippedRef struct {
	File    string
	ESName  string
	Service string
	Reason  SkipReason
	Detail  string
}

// Discovery is the outcome of a scan before Vault resolution: the references
// that will be checked, and every reference that will not be.
type Discovery struct {
	Refs    []SecretRef
	Skipped []SkippedRef
}

// Found returns every reference the scan saw, checked or not.
func (d Discovery) Found() int { return len(d.Refs) + len(d.Skipped) }

// SkipCounts tallies skipped references by reason.
func (d Discovery) SkipCounts() map[SkipReason]int {
	counts := map[SkipReason]int{}
	for _, s := range d.Skipped {
		counts[s.Reason]++
	}
	return counts
}

// DiscoverSecretRefs walks a tenants tree and returns every ExternalSecret
// reference in it, split into those resolvable against the named
// ClusterSecretStore and those skipped with a reason.
//
// The tree is the source of truth rather than the cluster, so a reference
// added on an unmerged branch is checked before it can land — applied state
// cannot see one, which made a live scan unusable as a merge gate.
func DiscoverSecretRefs(root, storeName string) (Discovery, error) {
	if storeName == "" {
		storeName = "vault"
	}
	enabled, err := enabledServices(root)
	if err != nil {
		return Discovery{}, err
	}

	var d Discovery
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if ext := filepath.Ext(path); ext != ".yaml" && ext != ".yml" {
			return nil
		}
		objs, err := decodeExternalSecrets(path)
		if err != nil {
			return err
		}
		tenant, service := attributeService(root, path)
		for _, obj := range objs {
			// A manifest outside tenants/<tenant>/services/<service>/ cannot be
			// attributed to a service entry; scan it rather than skip it, so an
			// unattributable ref fails loudly instead of vanishing.
			applied := service == "" || enabled[tenant+"/"+service]
			refs, skipped := classifyExternalSecret(obj, path, service, storeName, applied)
			d.Refs = append(d.Refs, refs...)
			d.Skipped = append(d.Skipped, skipped...)
		}
		return nil
	})
	if err != nil {
		return Discovery{}, err
	}

	sort.SliceStable(d.Refs, func(i, j int) bool { return refLess(d.Refs[i], d.Refs[j]) })
	sort.SliceStable(d.Skipped, func(i, j int) bool {
		a, b := d.Skipped[i], d.Skipped[j]
		if a.Reason != b.Reason {
			return a.Reason < b.Reason
		}
		if a.File != b.File {
			return a.File < b.File
		}
		return a.Detail < b.Detail
	})
	return d, nil
}

func refLess(a, b SecretRef) bool {
	if a.File != b.File {
		return a.File < b.File
	}
	if a.ESName != b.ESName {
		return a.ESName < b.ESName
	}
	if a.VaultKey != b.VaultKey {
		return a.VaultKey < b.VaultKey
	}
	return a.Property < b.Property
}

// classifyExternalSecret splits one ExternalSecret object into resolvable refs
// and skipped refs. It reads the same map shape for both sources: an
// unstructured cluster object and a decoded manifest are both map[string]any,
// so repo and cluster scans cannot drift in what they consider a reference.
func classifyExternalSecret(obj map[string]any, file, service, storeName string, applied bool) ([]SecretRef, []SkippedRef) {
	spec, _ := obj["spec"].(map[string]any)
	md, _ := obj["metadata"].(map[string]any)
	esName := stringField(md, "name")
	namespace := stringField(md, "namespace")

	storeRef, _ := spec["secretStoreRef"].(map[string]any)
	storeKind := stringField(storeRef, "kind")
	store := stringField(storeRef, "name")

	skipAll := func(reason SkipReason, detail string) []SkippedRef {
		var out []SkippedRef
		for _, entry := range dataEntries(obj) {
			out = append(out, SkippedRef{
				File: file, ESName: esName, Service: service, Reason: reason,
				Detail: fmt.Sprintf("%s (secretKey=%s)", detail, stringField(entry, "secretKey")),
			})
		}
		for i := range dataFromEntries(obj) {
			out = append(out, SkippedRef{
				File: file, ESName: esName, Service: service, Reason: reason,
				Detail: fmt.Sprintf("%s (dataFrom[%d])", detail, i),
			})
		}
		return out
	}

	switch {
	case !applied:
		return nil, skipAll(SkipServiceNotEnabled,
			fmt.Sprintf("service %q has no entry in tenant.yaml", service))
	case storeKind != "ClusterSecretStore" || store != storeName:
		return nil, skipAll(SkipStoreNotVault,
			fmt.Sprintf("bound to %s/%s", storeKind, store))
	}

	var refs []SecretRef
	var skipped []SkippedRef
	for _, entry := range dataEntries(obj) {
		remote, _ := entry["remoteRef"].(map[string]any)
		key := stringField(remote, "key")
		if key == "" {
			skipped = append(skipped, SkippedRef{
				File: file, ESName: esName, Service: service, Reason: SkipNoRemoteKey,
				Detail: fmt.Sprintf("no remoteRef.key (secretKey=%s)", stringField(entry, "secretKey")),
			})
			continue
		}
		refs = append(refs, SecretRef{
			File:      file,
			Namespace: namespace,
			ESName:    esName,
			VaultKey:  key,
			Property:  stringField(remote, "property"),
		})
	}
	for i, from := range dataFromEntries(obj) {
		skipped = append(skipped, SkippedRef{
			File: file, ESName: esName, Service: service, Reason: SkipDataFromPattern,
			Detail: fmt.Sprintf("dataFrom[%d] %s names no single property", i, dataFromTarget(from)),
		})
	}
	return refs, skipped
}

func dataFromEntries(obj map[string]any) []map[string]any {
	spec, _ := obj["spec"].(map[string]any)
	raw, _ := spec["dataFrom"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, e := range raw {
		if m, ok := e.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// dataFromTarget describes a dataFrom source for the skip message: the
// extracted key, the find pattern, or the source kind alone.
func dataFromTarget(from map[string]any) string {
	if extract, ok := from["extract"].(map[string]any); ok {
		if key := stringField(extract, "key"); key != "" {
			return "extract key=" + key
		}
		return "extract"
	}
	if _, ok := from["find"]; ok {
		return "find"
	}
	if _, ok := from["sourceRef"]; ok {
		return "sourceRef generator"
	}
	return "unrecognized source"
}

// enabledServices maps "<tenant>/<service>" to true for every service entry
// declared in a tenants/*/tenant.yaml.
func enabledServices(root string) (map[string]bool, error) {
	matches, err := filepath.Glob(filepath.Join(root, "*", "tenant.yaml"))
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("no tenant.yaml files found under %s", root)
	}
	enabled := map[string]bool{}
	for _, m := range matches {
		t, err := LoadFile(m)
		if err != nil {
			return nil, fmt.Errorf("parsing %s: %w", m, err)
		}
		name := t.Name
		if name == "" {
			name = filepath.Base(filepath.Dir(m))
		}
		for _, svc := range t.Services {
			enabled[name+"/"+svc.Name] = true
		}
	}
	return enabled, nil
}

// attributeService derives the owning tenant and service from a manifest path
// of the form <root>/<tenant>/services/<service>/... Returns an empty service
// when the path does not have that shape.
func attributeService(root, path string) (tenant, service string) {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return "", ""
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) < 4 || parts[1] != "services" {
		if len(parts) > 0 {
			return parts[0], ""
		}
		return "", ""
	}
	return parts[0], parts[2]
}

func decodeExternalSecrets(path string) ([]map[string]any, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	dec := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(raw), 4096)
	var out []map[string]any
	for {
		var obj map[string]any
		if err := dec.Decode(&obj); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			// Matches LintExternalSecrets: a document that does not decode as a
			// manifest map (templated or non-k8s YAML) is not scannable here, so
			// stop on this file rather than failing the whole run.
			break
		}
		if kind, _ := obj["kind"].(string); kind != "ExternalSecret" {
			continue
		}
		out = append(out, obj)
	}
	return out, nil
}
