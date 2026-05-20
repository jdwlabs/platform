package tenants

import (
	"context"
	"errors"
	"fmt"
	"sort"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/jdwlabs/platform/internal/vault"
)

var externalSecretGVR = schema.GroupVersionResource{
	Group:    "external-secrets.io",
	Version:  "v1",
	Resource: "externalsecrets",
}

// SecretRef is one (Vault key, property) reference made by an ExternalSecret.
type SecretRef struct {
	Namespace  string
	ESName     string
	VaultKey   string
	Property   string
}

// SecretIssue describes a single mismatch between an ExternalSecret reference
// and live Vault state.
type SecretIssue struct {
	SecretRef
	Kind   string // "vault-key-missing" | "property-missing" | "vault-error"
	Detail string
}

// VerifyReport is the aggregate result of VerifySecrets.
type VerifyReport struct {
	Checked int
	Issues  []SecretIssue
}

// HasIssues returns true when any issue was recorded.
func (r VerifyReport) HasIssues() bool { return len(r.Issues) > 0 }

// VerifySecrets lists all ExternalSecrets backed by the named ClusterSecretStore
// and checks that every referenced Vault key + property exists in the live
// Vault instance. Only spec.data[].remoteRef refs are inspected; spec.dataFrom
// patterns are skipped (out of scope for now — most platform/tenant secrets use
// explicit data[]).
//
// The vault client should already be authenticated with a token that can read
// the kv mount (root token from external-secrets/vault-token works).
func VerifySecrets(ctx context.Context, dyn dynamic.Interface, vc *vault.Client, kvMount, storeName string) (VerifyReport, error) {
	if kvMount == "" {
		kvMount = "kv"
	}
	if storeName == "" {
		storeName = "vault"
	}

	list, err := dyn.Resource(externalSecretGVR).Namespace("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return VerifyReport{}, fmt.Errorf("list ExternalSecrets: %w", err)
	}

	var refs []SecretRef
	for _, item := range list.Items {
		storeKind, _, _ := unstructured.NestedString(item.Object, "spec", "secretStoreRef", "kind")
		store, _, _ := unstructured.NestedString(item.Object, "spec", "secretStoreRef", "name")
		if storeKind != "ClusterSecretStore" || store != storeName {
			continue
		}
		data, _, _ := unstructured.NestedSlice(item.Object, "spec", "data")
		for _, d := range data {
			m, ok := d.(map[string]any)
			if !ok {
				continue
			}
			key, _ := nestedStr(m, "remoteRef", "key")
			prop, _ := nestedStr(m, "remoteRef", "property")
			if key == "" {
				continue
			}
			refs = append(refs, SecretRef{
				Namespace: item.GetNamespace(),
				ESName:    item.GetName(),
				VaultKey:  key,
				Property:  prop,
			})
		}
	}

	report := VerifyReport{Checked: len(refs)}
	cache := map[string]map[string]any{}
	cacheErr := map[string]error{}
	for _, ref := range refs {
		data, hit := cache[ref.VaultKey]
		if !hit {
			d, err := vc.GetKV(ctx, kvMount, ref.VaultKey)
			if err != nil {
				cacheErr[ref.VaultKey] = err
				cache[ref.VaultKey] = nil
			} else {
				cache[ref.VaultKey] = d
				data = d
			}
		}
		if e, ok := cacheErr[ref.VaultKey]; ok {
			report.Issues = append(report.Issues, SecretIssue{
				SecretRef: ref,
				Kind:      classifyVaultError(e),
				Detail:    e.Error(),
			})
			continue
		}
		if ref.Property != "" {
			if _, has := data[ref.Property]; !has {
				report.Issues = append(report.Issues, SecretIssue{
					SecretRef: ref,
					Kind:      "property-missing",
					Detail:    fmt.Sprintf("Vault key %q has no field %q", ref.VaultKey, ref.Property),
				})
			}
		}
	}

	sort.SliceStable(report.Issues, func(i, j int) bool {
		a, b := report.Issues[i], report.Issues[j]
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		if a.ESName != b.ESName {
			return a.ESName < b.ESName
		}
		return a.Property < b.Property
	})
	return report, nil
}

func classifyVaultError(err error) string {
	if isVaultNotFound(err) {
		return "vault-key-missing"
	}
	return "vault-error"
}

// isVaultNotFound returns true for a Vault KVv2 "not found" response. The
// hashicorp/vault api wraps these as plain errors with status 404, so we
// detect via k8s-style helpers + string fallback.
func isVaultNotFound(err error) bool {
	if err == nil {
		return false
	}
	if k8serrors.IsNotFound(err) { // unlikely but cheap
		return true
	}
	// hashicorp/vault api returns a *api.ResponseError with StatusCode 404 for
	// missing KV-v2 paths. The error string includes "secret not found" in some
	// versions, "Code: 404" in others. Cover both.
	var se interface{ Error() string }
	if errors.As(err, &se) {
		s := se.Error()
		if containsAny(s, []string{"404", "not found", "no value found"}) {
			return true
		}
	}
	return false
}

func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if len(sub) > 0 && stringContains(s, sub) {
			return true
		}
	}
	return false
}

// stringContains avoids importing strings to keep deps tight.
func stringContains(s, sub string) bool {
	if len(sub) > len(s) {
		return false
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func nestedStr(m map[string]any, path ...string) (string, bool) {
	cur := any(m)
	for _, k := range path {
		mm, ok := cur.(map[string]any)
		if !ok {
			return "", false
		}
		cur = mm[k]
	}
	s, ok := cur.(string)
	return s, ok
}
