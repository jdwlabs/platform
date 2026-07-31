// Package gitsync reads and deletes Grafana Git Sync provisioning resources.
//
// Connection and Repository look like Kubernetes objects and use a
// Kubernetes-style API, but they live inside Grafana's own API server: kubectl
// cannot see them and ArgoCD cannot reconcile them. Their health blocks are the
// only place a broken sync surfaces, and neither error names the workflow that
// causes it — the connection's "GitHub App lacks required 'webhooks' permission"
// is derived from the bound repositories' workflows, not from any missing grant.
// Reporting both health blocks verbatim is therefore the whole point of the read
// path.
package gitsync

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	// KindConnection and KindRepository are the singular names the CLI accepts;
	// the API paths are their plurals.
	KindConnection = "connection"
	KindRepository = "repository"

	// DefaultNamespace is the Grafana provisioning namespace. It is Grafana's
	// own namespace concept, unrelated to the Kubernetes namespace Grafana runs in.
	DefaultNamespace = "default"

	provisioningAPI = "/apis/provisioning.grafana.app/v0alpha1"
	dashboardAPI    = "/apis/dashboard.grafana.app/v1beta1"

	// Dashboards provisioned by the ConfigMap sidecar carry this manager value
	// and are never owned by a Git Sync repository.
	classicProvisioning = "classic-file-provisioning"

	maxErrorBody = 400
)

// Resource is one Git Sync provisioning resource, reduced to what an operator
// needs to decide whether Git Sync is working.
type Resource struct {
	Kind string
	Name string
	// Healthy is only ever true when the API said so explicitly.
	Healthy bool
	// HealthKnown is false when the resource carries no health block at all.
	// Absent health is not healthy — it is unreported, and treating the two the
	// same is how a broken sync passes a gate.
	HealthKnown   bool
	HealthMessage string
	SyncState     string
	// ConnectionRef is the connection a repository binds to. Empty on connections.
	ConnectionRef string
}

// Client talks to Grafana's HTTP API with the instance's admin credentials.
type Client struct {
	base      string
	user      string
	pass      string
	namespace string
	http      *http.Client
}

func NewClient(base, user, pass, namespace string) *Client {
	if namespace == "" {
		namespace = DefaultNamespace
	}
	return &Client{
		base:      strings.TrimRight(base, "/"),
		user:      user,
		pass:      pass,
		namespace: namespace,
		http:      &http.Client{Timeout: 30 * time.Second},
	}
}

// Kinds returns the resource kinds this client understands, in the order they
// must be read and reported (connection first, because a repository binds to it).
func Kinds() []string { return []string{KindConnection, KindRepository} }

func pluralOf(kind string) (string, error) {
	switch kind {
	case KindConnection:
		return "connections", nil
	case KindRepository:
		return "repositories", nil
	default:
		return "", fmt.Errorf("unknown kind %s; valid: %s", kind, strings.Join(Kinds(), ", "))
	}
}

// List returns every resource of one kind.
func (c *Client) List(ctx context.Context, kind string) ([]Resource, error) {
	plural, err := pluralOf(kind)
	if err != nil {
		return nil, err
	}
	body, err := c.do(ctx, http.MethodGet, c.provisioningURL(plural, ""))
	if err != nil {
		return nil, err
	}
	var payload struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decode %s collection: %w", plural, err)
	}
	out := make([]Resource, 0, len(payload.Items))
	for _, raw := range payload.Items {
		res, err := parseResource(kind, raw)
		if err != nil {
			return nil, err
		}
		out = append(out, res)
	}
	return out, nil
}

// Delete removes one resource. An already-absent resource is the desired end
// state, so the call is safe to repeat.
func (c *Client) Delete(ctx context.Context, kind, name string) error {
	plural, err := pluralOf(kind)
	if err != nil {
		return err
	}
	if name == "" {
		return fmt.Errorf("delete %s: no name given", kind)
	}
	_, err = c.do(ctx, http.MethodDelete, c.provisioningURL(plural, name))
	if isNotFound(err) {
		return nil
	}
	return err
}

// DashboardsOwnedBy returns the dashboards a repository manages. Deleting a
// repository that still owns dashboards lets its remove-orphan-resources
// finalizer collect them, so this is a precondition, not a diagnostic — and a
// failed request must surface as an error rather than as an empty list.
func (c *Client) DashboardsOwnedBy(ctx context.Context, repository string) ([]string, error) {
	url := fmt.Sprintf("%s%s/namespaces/%s/dashboards", c.base, dashboardAPI, c.namespace)
	body, err := c.do(ctx, http.MethodGet, url)
	if err != nil {
		return nil, fmt.Errorf("read dashboards to check ownership: %w", err)
	}
	var payload struct {
		Items []struct {
			Metadata struct {
				Name        string            `json:"name"`
				Annotations map[string]string `json:"annotations"`
			} `json:"metadata"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decode dashboard collection: %w", err)
	}
	var owned []string
	for _, item := range payload.Items {
		if ownedByRepository(item.Metadata.Annotations, repository) {
			owned = append(owned, item.Metadata.Name)
		}
	}
	return owned, nil
}

// RepositoriesUsingConnection names the repositories bound to a connection.
// Deleting the connection first strands them, which is why the delete path
// enforces repository-before-connection ordering.
func RepositoriesUsingConnection(repos []Resource, connection string) []string {
	var out []string
	for _, r := range repos {
		if r.ConnectionRef == connection {
			out = append(out, r.Name)
		}
	}
	return out
}

func ownedByRepository(annotations map[string]string, repository string) bool {
	if repository == "" {
		return false
	}
	// Two annotation generations are in play, so both are checked; the sidecar's
	// own marker is excluded explicitly rather than by omission.
	if v := annotations["grafana.app/managedBy"]; v != "" && v != classicProvisioning {
		if v == repository {
			return true
		}
	}
	return annotations["grafana.app/managerId"] == repository
}

func (c *Client) provisioningURL(plural, name string) string {
	url := fmt.Sprintf("%s%s/namespaces/%s/%s", c.base, provisioningAPI, c.namespace, plural)
	if name != "" {
		url += "/" + name
	}
	return url
}

type notFoundError struct{ url string }

func (e *notFoundError) Error() string { return "not found: " + e.url }

func isNotFound(err error) bool {
	_, ok := err.(*notFoundError)
	return ok
}

func (c *Client) do(ctx context.Context, method, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.SetBasicAuth(c.user, c.pass)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("reach Grafana at %s: %w", c.base, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, readErr := io.ReadAll(resp.Body)
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, &notFoundError{url: url}
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("Grafana rejected the admin credentials (HTTP %d)", resp.StatusCode)
	case resp.StatusCode >= 300:
		return nil, fmt.Errorf("Grafana returned HTTP %d: %s", resp.StatusCode, trimBody(body))
	}
	if readErr != nil {
		return nil, fmt.Errorf("read response from %s: %w", url, readErr)
	}
	return body, nil
}

func trimBody(b []byte) string {
	s := strings.TrimSpace(string(b))
	if s == "" {
		return "(empty response)"
	}
	if len(s) > maxErrorBody {
		return s[:maxErrorBody] + fmt.Sprintf("... (truncated, %d bytes total)", len(s))
	}
	return s
}

func parseResource(kind string, raw json.RawMessage) (Resource, error) {
	var item struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
		Spec struct {
			Connection struct {
				Name string `json:"name"`
			} `json:"connection"`
		} `json:"spec"`
		Status struct {
			Health *struct {
				Healthy bool            `json:"healthy"`
				Message json.RawMessage `json:"message"`
				Error   string          `json:"error"`
			} `json:"health"`
			Sync struct {
				State   string          `json:"state"`
				Message json.RawMessage `json:"message"`
			} `json:"sync"`
		} `json:"status"`
	}
	if err := json.Unmarshal(raw, &item); err != nil {
		return Resource{}, fmt.Errorf("decode %s: %w", kind, err)
	}
	if item.Metadata.Name == "" {
		return Resource{}, fmt.Errorf("a %s in the collection has no metadata.name", kind)
	}
	res := Resource{
		Kind:          kind,
		Name:          item.Metadata.Name,
		SyncState:     item.Status.Sync.State,
		ConnectionRef: item.Spec.Connection.Name,
	}
	if item.Status.Health == nil {
		res.HealthMessage = "status.health absent — Grafana has not reported health for this resource"
		return res, nil
	}
	res.HealthKnown = true
	res.Healthy = item.Status.Health.Healthy
	res.HealthMessage = joinMessages(item.Status.Health.Message)
	if res.HealthMessage == "" {
		res.HealthMessage = item.Status.Health.Error
	}
	if res.HealthMessage == "" && res.Healthy {
		res.HealthMessage = "healthy"
	}
	return res, nil
}

// joinMessages accepts the field as either a string or a list of strings: the
// list form carries the detail that names the actual cause, so dropping the
// non-scalar shape would discard the useful half of the diagnosis.
func joinMessages(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var one string
	if err := json.Unmarshal(raw, &one); err == nil {
		return one
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err == nil {
		return strings.Join(many, "; ")
	}
	return strings.TrimSpace(string(raw))
}
