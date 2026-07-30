package tenants

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
// File is set when the ref came from a manifest on disk, empty for a ref read
// off a live cluster object.
type SecretRef struct {
	File      string
	Namespace string
	ESName    string
	VaultKey  string
	Property  string
}

// SecretIssue describes a single mismatch between an ExternalSecret reference
// and live Vault state.
type SecretIssue struct {
	SecretRef
	Kind   string // "vault-key-missing" | "property-missing" | "vault-error"
	Detail string
}

// VerifyReport is the aggregate result of a verification scan. Found and
// Checked are reported separately: a summary that states only the checked count
// reads as full coverage even when references were skipped.
type VerifyReport struct {
	Found   int
	Checked int
	Skipped []SkippedRef
	Issues  []SecretIssue
}

// HasIssues returns true when any issue was recorded.
func (r VerifyReport) HasIssues() bool { return len(r.Issues) > 0 }

// Summary is the one-line aggregate for the summary event. It always states
// found, checked, and issue counts, and names every skipped class with its
// count so a partial scan can never read as a complete one.
func (r VerifyReport) Summary() string {
	b := fmt.Sprintf("%d refs found; %d checked", r.Found, r.Checked)
	if len(r.Skipped) > 0 {
		counts := map[SkipReason]int{}
		for _, s := range r.Skipped {
			counts[s.Reason]++
		}
		reasons := make([]string, 0, len(counts))
		for reason := range counts {
			reasons = append(reasons, string(reason))
		}
		sort.Strings(reasons)
		parts := make([]string, 0, len(reasons))
		for _, reason := range reasons {
			parts = append(parts, fmt.Sprintf("%s=%d", reason, counts[SkipReason(reason)]))
		}
		b += fmt.Sprintf("; %d not checked (%s)", len(r.Skipped), strings.Join(parts, ", "))
	}
	return b + fmt.Sprintf("; %d issues", len(r.Issues))
}

// SkipLines renders one line per skipped class, naming the owning services (or
// ExternalSecrets, when the ref has no service) so a reader can tell which refs
// went unchecked without re-deriving it from the tree.
func (r VerifyReport) SkipLines() []string {
	owners := map[SkipReason]map[string]bool{}
	counts := map[SkipReason]int{}
	for _, s := range r.Skipped {
		counts[s.Reason]++
		owner := s.Service
		if owner == "" {
			owner = s.ESName
		}
		if owners[s.Reason] == nil {
			owners[s.Reason] = map[string]bool{}
		}
		if owner != "" {
			owners[s.Reason][owner] = true
		}
	}
	reasons := make([]string, 0, len(counts))
	for reason := range counts {
		reasons = append(reasons, string(reason))
	}
	sort.Strings(reasons)

	lines := make([]string, 0, len(reasons))
	for _, reason := range reasons {
		names := make([]string, 0, len(owners[SkipReason(reason)]))
		for name := range owners[SkipReason(reason)] {
			names = append(names, name)
		}
		sort.Strings(names)
		line := fmt.Sprintf("%s: %d ref(s)", reason, counts[SkipReason(reason)])
		if len(names) > 0 {
			line += " — " + strings.Join(names, ", ")
		}
		lines = append(lines, line)
	}
	return lines
}

// VerifySecrets checks the ExternalSecrets currently applied to the cluster: it
// resolves every spec.data[].remoteRef against live Vault and reports every
// other reference as a named skip. It reflects applied state only, so a
// reference that exists in the tree but was never applied — a dormant service,
// or anything on an unmerged branch — is invisible to it. Use VerifyRepoSecrets
// to gate a change before it lands.
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

	var d Discovery
	for _, item := range list.Items {
		// Applied by definition: the object was read off the cluster.
		refs, skipped := classifyExternalSecret(item.Object, "", "", storeName, true)
		d.Refs = append(d.Refs, refs...)
		d.Skipped = append(d.Skipped, skipped...)
	}
	return resolveRefs(ctx, vc, kvMount, d), nil
}

// VerifyRepoSecrets scans the tenants tree at root and checks every
// ExternalSecret reference in it against live Vault. Unlike VerifySecrets it
// sees references that are not applied to the cluster, which is what makes it
// usable as a pre-merge gate.
func VerifyRepoSecrets(ctx context.Context, vc *vault.Client, root, kvMount, storeName string) (VerifyReport, error) {
	if kvMount == "" {
		kvMount = "kv"
	}
	if storeName == "" {
		storeName = "vault"
	}
	d, err := DiscoverSecretRefs(root, storeName)
	if err != nil {
		return VerifyReport{}, err
	}
	return resolveRefs(ctx, vc, kvMount, d), nil
}

// resolveRefs reads each discovered reference's Vault path once and records an
// issue per reference that does not resolve. Skipped references are carried
// through to the report untouched — they are never read.
func resolveRefs(ctx context.Context, vc *vault.Client, kvMount string, d Discovery) VerifyReport {
	refs := d.Refs
	report := VerifyReport{
		Found:   d.Found(),
		Checked: len(refs),
		Skipped: d.Skipped,
	}
	cache := map[string]map[string]any{}
	cacheErr := map[string]error{}
	for _, ref := range refs {
		data, hit := cache[ref.VaultKey]
		if !hit {
			secret, err := vc.GetKV(ctx, kvMount, ref.VaultKey)
			if err != nil {
				cacheErr[ref.VaultKey] = err
				cache[ref.VaultKey] = nil
			} else {
				cache[ref.VaultKey] = secret
				data = secret
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
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		if a.ESName != b.ESName {
			return a.ESName < b.ESName
		}
		return a.Property < b.Property
	})
	return report
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
		if sub != "" && strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
