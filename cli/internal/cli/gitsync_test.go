package cli

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/jdwlabs/platform/internal/k8s"
)

// The unhealthy fixture reproduces the pair of messages that made the incident
// hard to read: the connection blames a missing webhooks permission while the
// real cause is the repository's "write" workflow.
const unhealthyRepositories = `{"items":[{
	"metadata":{"name":"platform-dashboards"},
	"spec":{"connection":{"name":"jdwlabs-platform-github"}},
	"status":{"health":{"healthy":false,"message":["branch \"main\" has protection rules that prevent direct pushes","the \"write\" workflow is not compatible with this branch"]},"sync":{"state":"error"}}}]}`

const unhealthyConnections = `{"items":[{
	"metadata":{"name":"jdwlabs-platform-github"},
	"status":{"health":{"healthy":false,"error":"GitHub App lacks required 'webhooks' permission: requires 'write', has ''"},"sync":{"state":"error"}}}]}`

const healthyRepositories = `{"items":[{
	"metadata":{"name":"platform-dashboards"},
	"spec":{"connection":{"name":"jdwlabs-platform-github"}},
	"status":{"health":{"healthy":true},"sync":{"state":"success"}}}]}`

const healthyConnections = `{"items":[{
	"metadata":{"name":"jdwlabs-platform-github"},
	"status":{"health":{"healthy":true},"sync":{"state":"success"}}}]}`

// Three repositories on one connection is the per-tenant folder shape: one
// GitHub App reaching one repo, a separate Repository per synced path.
const sharedConnectionRepositories = `{"items":[{
	"metadata":{"name":"platform-dashboards"},
	"spec":{"connection":{"name":"jdwlabs-platform-github"}},
	"status":{"health":{"healthy":true},"sync":{"state":"success"}}},{
	"metadata":{"name":"jdwlabs-dashboards"},
	"spec":{"connection":{"name":"jdwlabs-platform-github"}},
	"status":{"health":{"healthy":true},"sync":{"state":"success"}}},{
	"metadata":{"name":"dotablaze-tech-dashboards"},
	"spec":{"connection":{"name":"jdwlabs-platform-github"}},
	"status":{"health":{"healthy":true},"sync":{"state":"success"}}}]}`

const noDashboards = `{"items":[]}`

const ownedDashboards = `{"items":[{"metadata":{"name":"vault-overview",
	"annotations":{"grafana.app/managerKind":"repo","grafana.app/managerId":"platform-dashboards"}}}]}`

type grafanaStub struct {
	repositories string
	connections  string
	dashboards   string
	deleted      []string
}

func (s *grafanaStub) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			s.deleted = append(s.deleted, r.URL.Path)
			w.WriteHeader(http.StatusOK)
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/repositories"):
			_, _ = w.Write([]byte(s.repositories))
		case strings.HasSuffix(r.URL.Path, "/connections"):
			_, _ = w.Write([]byte(s.connections))
		case strings.HasSuffix(r.URL.Path, "/dashboards"):
			_, _ = w.Write([]byte(s.dashboards))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func grafanaKubeObjects() []runtime.Object {
	return []runtime.Object{
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "grafana-admin-credentials", Namespace: "monitoring"},
			Data: map[string][]byte{
				"admin-user":     []byte("admin"),
				"admin-password": []byte("s3cret"),
			},
		},
	}
}

func grafanaApp() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "Application",
		"metadata":   map[string]interface{}{"name": "platform-grafana", "namespace": "argocd"},
	}}
}

func runGitSync(t *testing.T, stub *grafanaStub, args ...string) (string, error) {
	t.Helper()
	srv := stub.start(t)
	kc := k8s.NewFake(grafanaKubeObjects()...)
	dc := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}: "ApplicationList",
		},
		grafanaApp(),
	)
	root := NewRootForTest(kc, dc)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(append(args, "--grafana-addr", srv.URL))
	err := root.Execute()
	return out.String(), err
}

func TestGitSyncStatus_HealthyExitsZeroWithDefinitiveZeroIssues(t *testing.T) {
	stub := &grafanaStub{repositories: healthyRepositories, connections: healthyConnections, dashboards: noDashboards}
	out, err := runGitSync(t, stub, "gitsync", "status")
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, out)
	}
	if !strings.Contains(out, "count: 2 total (2 healthy / 0 unhealthy / 0 health unknown)") {
		t.Errorf("missing aggregate count:\n%s", out)
	}
	if !strings.Contains(out, "gitsync[2]{kind,name,healthy,syncState}:") {
		t.Errorf("missing TOON table:\n%s", out)
	}
	if !strings.Contains(out, "result: 0 issues — all 2 resource(s) healthy") {
		t.Errorf("a clean result must say so definitively:\n%s", out)
	}
	if strings.Contains(out, "unhealthy[") {
		t.Errorf("no unhealthy table should appear:\n%s", out)
	}
}

func TestGitSyncStatus_UnhealthyExitsNonZeroAndPrintsBothMessages(t *testing.T) {
	stub := &grafanaStub{repositories: unhealthyRepositories, connections: unhealthyConnections, dashboards: noDashboards}
	out, err := runGitSync(t, stub, "gitsync", "status")
	if err == nil {
		t.Fatalf("an unhealthy resource must exit non-zero\n%s", out)
	}
	if !strings.Contains(out, "unhealthy[2]{kind,name,health}:") {
		t.Errorf("health messages must be reported without --full:\n%s", out)
	}
	// Both halves of the diagnosis have to survive: the misleading connection
	// error and the repository message that actually names the cause.
	if !strings.Contains(out, "webhooks") {
		t.Errorf("connection health message lost:\n%s", out)
	}
	if !strings.Contains(out, "workflow is not compatible") {
		t.Errorf("repository health message lost:\n%s", out)
	}
	if !strings.Contains(out, "result: 2 of 2 resource(s) unhealthy") {
		t.Errorf("missing result line:\n%s", out)
	}
}

func TestGitSyncStatus_BareNounReportsStatus(t *testing.T) {
	stub := &grafanaStub{repositories: healthyRepositories, connections: healthyConnections, dashboards: noDashboards}
	out, err := runGitSync(t, stub, "gitsync")
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, out)
	}
	if !strings.Contains(out, "gitsync[2]{kind,name,healthy,syncState}:") {
		t.Fatalf("bare noun should print content:\n%s", out)
	}
}

func TestGitSyncStatus_EmptyCollectionsAreAFinding(t *testing.T) {
	stub := &grafanaStub{repositories: `{"items":[]}`, connections: `{"items":[]}`, dashboards: noDashboards}
	out, err := runGitSync(t, stub, "gitsync", "status")
	if err == nil {
		t.Fatalf("no resources means not connected, which must not exit zero\n%s", out)
	}
	if !strings.Contains(out, "result: 0 resources — git sync is credentialed but not connected") {
		t.Errorf("the empty case must be stated explicitly:\n%s", out)
	}
}

func TestGitSyncStatus_MissingHealthBlockCountsAsUnknownNotHealthy(t *testing.T) {
	stub := &grafanaStub{
		repositories: `{"items":[{"metadata":{"name":"platform-dashboards"},"status":{}}]}`,
		connections:  `{"items":[]}`,
		dashboards:   noDashboards,
	}
	out, err := runGitSync(t, stub, "gitsync", "status")
	if err == nil {
		t.Fatalf("unreported health must not pass as healthy\n%s", out)
	}
	if !strings.Contains(out, "unknown") {
		t.Errorf("the row should read unknown:\n%s", out)
	}
	if !strings.Contains(out, "status.health") {
		t.Errorf("the absent field should be named:\n%s", out)
	}
}

func TestGitSyncStatus_FullIncludesHealthAndConnection(t *testing.T) {
	stub := &grafanaStub{repositories: healthyRepositories, connections: healthyConnections, dashboards: noDashboards}
	out, err := runGitSync(t, stub, "gitsync", "status", "--full")
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, out)
	}
	if !strings.Contains(out, "gitsync[2]{kind,name,healthy,syncState,health,connection}:") {
		t.Fatalf("--full should widen the table:\n%s", out)
	}
}

func TestGitSyncStatus_UnknownFieldAndFlagAreReportedOnStdout(t *testing.T) {
	stub := &grafanaStub{repositories: healthyRepositories, connections: healthyConnections, dashboards: noDashboards}

	out, err := runGitSync(t, stub, "gitsync", "status", "--fields", "kind,bogus")
	if err == nil {
		t.Fatalf("expected an error\n%s", out)
	}
	if !strings.Contains(out, "syncState") {
		t.Errorf("error should name the valid fields:\n%s", out)
	}

	out, err = runGitSync(t, stub, "gitsync", "status", "--stat", "x")
	if err == nil {
		t.Fatalf("expected an unknown-flag error\n%s", out)
	}
	if !strings.Contains(out, "unknown flag") || !strings.Contains(out, "--fields") {
		t.Errorf("unknown flag must reach stdout with the valid set:\n%s", out)
	}
}

func TestGitSyncDelete_NeedsExplicitIntent(t *testing.T) {
	stub := &grafanaStub{repositories: healthyRepositories, connections: healthyConnections, dashboards: noDashboards}
	out, err := runGitSync(t, stub, "gitsync", "delete", "--kind", "repository", "--name", "platform-dashboards")
	if err == nil {
		t.Fatalf("delete must refuse without --confirm or --dry-run\n%s", out)
	}
	if !strings.Contains(out, "--confirm") || !strings.Contains(out, "--dry-run") {
		t.Errorf("refusal should name both escape hatches:\n%s", out)
	}
	if len(stub.deleted) != 0 {
		t.Fatalf("nothing should have been deleted: %v", stub.deleted)
	}
}

func TestGitSyncDelete_RequiresKindAndName(t *testing.T) {
	stub := &grafanaStub{repositories: healthyRepositories, connections: healthyConnections, dashboards: noDashboards}
	if _, err := runGitSync(t, stub, "gitsync", "delete", "--confirm"); err == nil {
		t.Fatal("expected an error when --kind and --name are missing")
	}
	out, err := runGitSync(t, stub, "gitsync", "delete", "--kind", "dashboard", "--name", "x", "--confirm")
	if err == nil {
		t.Fatalf("expected an error for an unsupported kind\n%s", out)
	}
	if !strings.Contains(out, "repository") {
		t.Errorf("error should list the valid kinds:\n%s", out)
	}
}

func TestGitSyncDelete_RefusesRepositoryOwningDashboards(t *testing.T) {
	stub := &grafanaStub{repositories: healthyRepositories, connections: healthyConnections, dashboards: ownedDashboards}
	out, err := runGitSync(t, stub, "gitsync", "delete",
		"--kind", "repository", "--name", "platform-dashboards", "--confirm")
	if err == nil {
		t.Fatalf("a repository owning dashboards must not be deleted\n%s", out)
	}
	if !strings.Contains(out, "vault-overview") {
		t.Errorf("refusal should name the dashboards at risk:\n%s", out)
	}
	if len(stub.deleted) != 0 {
		t.Fatalf("nothing should have been deleted: %v", stub.deleted)
	}
}

func TestGitSyncDelete_OwnedDashboardsOverrideProceeds(t *testing.T) {
	stub := &grafanaStub{repositories: healthyRepositories, connections: healthyConnections, dashboards: ownedDashboards}
	out, err := runGitSync(t, stub, "gitsync", "delete", "--kind", "repository",
		"--name", "platform-dashboards", "--confirm", "--allow-owned-dashboards")
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, out)
	}
	if len(stub.deleted) != 1 || !strings.HasSuffix(stub.deleted[0], "/repositories/platform-dashboards") {
		t.Fatalf("deleted = %v", stub.deleted)
	}
}

func TestGitSyncDelete_RefusesConnectionStillReferenced(t *testing.T) {
	stub := &grafanaStub{repositories: healthyRepositories, connections: healthyConnections, dashboards: noDashboards}
	out, err := runGitSync(t, stub, "gitsync", "delete",
		"--kind", "connection", "--name", "jdwlabs-platform-github", "--confirm")
	if err == nil {
		t.Fatalf("ordering must be enforced: the repository references this connection\n%s", out)
	}
	if !strings.Contains(out, "platform-dashboards") {
		t.Errorf("refusal should name the referencing repository:\n%s", out)
	}
	if len(stub.deleted) != 0 {
		t.Fatalf("nothing should have been deleted: %v", stub.deleted)
	}
}

func TestGitSyncDelete_AbsentResourceIsANoOp(t *testing.T) {
	stub := &grafanaStub{repositories: `{"items":[]}`, connections: `{"items":[]}`, dashboards: noDashboards}
	out, err := runGitSync(t, stub, "gitsync", "delete", "--kind", "repository", "--name", "gone", "--confirm")
	if err != nil {
		t.Fatalf("an absent resource is the desired state: %v\n%s", err, out)
	}
	if !strings.Contains(out, "already absent (no-op)") {
		t.Errorf("the no-op must be stated:\n%s", out)
	}
	if len(stub.deleted) != 0 {
		t.Fatalf("no request should have been sent: %v", stub.deleted)
	}
}

func TestGitSyncDelete_DryRunMutatesNothing(t *testing.T) {
	stub := &grafanaStub{repositories: healthyRepositories, connections: healthyConnections, dashboards: noDashboards}
	out, err := runGitSync(t, stub, "gitsync", "delete",
		"--kind", "repository", "--name", "platform-dashboards", "--dry-run")
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, out)
	}
	if !strings.Contains(out, "mode: dry-run") || !strings.Contains(out, "nothing was mutated") {
		t.Errorf("dry-run must announce itself:\n%s", out)
	}
	if len(stub.deleted) != 0 {
		t.Fatalf("dry-run deleted something: %v", stub.deleted)
	}
}

func TestGitSyncRecreate_DeletesRepositoryBeforeConnection(t *testing.T) {
	stub := &grafanaStub{repositories: healthyRepositories, connections: healthyConnections, dashboards: noDashboards}
	out, err := runGitSync(t, stub, "gitsync", "recreate", "--confirm")
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, out)
	}
	if len(stub.deleted) != 2 {
		t.Fatalf("expected two deletes, got %v", stub.deleted)
	}
	if !strings.HasSuffix(stub.deleted[0], "/repositories/platform-dashboards") {
		t.Errorf("the repository must be deleted first, got %v", stub.deleted)
	}
	if !strings.HasSuffix(stub.deleted[1], "/connections/jdwlabs-platform-github") {
		t.Errorf("the connection must be deleted second, got %v", stub.deleted)
	}
	if !strings.Contains(out, "order[2]{step,kind,name}:") {
		t.Errorf("the enforced order should be reported:\n%s", out)
	}
	if !strings.Contains(out, "ArgoCD refresh requested for platform-grafana") {
		t.Errorf("the resync request should be reported:\n%s", out)
	}
}

func TestGitSyncRecreate_DryRunShowsOrderAndMutatesNothing(t *testing.T) {
	stub := &grafanaStub{repositories: healthyRepositories, connections: healthyConnections, dashboards: noDashboards}
	out, err := runGitSync(t, stub, "gitsync", "recreate", "--dry-run")
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, out)
	}
	if !strings.Contains(out, "order[2]{step,kind,name}:") || !strings.Contains(out, "nothing was mutated") {
		t.Errorf("dry-run should show the order and mutate nothing:\n%s", out)
	}
	if len(stub.deleted) != 0 {
		t.Fatalf("dry-run deleted something: %v", stub.deleted)
	}
}

func TestGitSyncRecreate_RefusesWhenRepositoryOwnsDashboards(t *testing.T) {
	stub := &grafanaStub{repositories: healthyRepositories, connections: healthyConnections, dashboards: ownedDashboards}
	out, err := runGitSync(t, stub, "gitsync", "recreate", "--confirm")
	if err == nil {
		t.Fatalf("expected a refusal\n%s", out)
	}
	if len(stub.deleted) != 0 {
		t.Fatalf("nothing should have been deleted: %v", stub.deleted)
	}
}

func TestGitSyncRecreate_SharedConnectionSurvivesRecreatingOneRepository(t *testing.T) {
	stub := &grafanaStub{repositories: sharedConnectionRepositories, connections: healthyConnections, dashboards: noDashboards}
	out, err := runGitSync(t, stub, "gitsync", "recreate", "--repository", "jdwlabs-dashboards", "--confirm")
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, out)
	}
	if len(stub.deleted) != 1 {
		t.Fatalf("only the named repository should be deleted, got %v", stub.deleted)
	}
	if !strings.HasSuffix(stub.deleted[0], "/repositories/jdwlabs-dashboards") {
		t.Errorf("wrong resource deleted: %v", stub.deleted)
	}
	for _, name := range []string{"platform-dashboards", "dotablaze-tech-dashboards"} {
		if !strings.Contains(out, name) {
			t.Errorf("the stranded-sibling reason should name %s:\n%s", name, out)
		}
	}
	if !strings.Contains(out, "retained:") {
		t.Errorf("keeping the shared connection should be reported, not inferred:\n%s", out)
	}
}

func TestGitSyncRecreate_LastRepositoryStillTakesTheConnection(t *testing.T) {
	stub := &grafanaStub{repositories: healthyRepositories, connections: healthyConnections, dashboards: noDashboards}
	out, err := runGitSync(t, stub, "gitsync", "recreate", "--dry-run")
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, out)
	}
	if !strings.Contains(out, "order[2]{step,kind,name}:") {
		t.Errorf("a sole repository must still plan the connection delete:\n%s", out)
	}
	if strings.Contains(out, "retained:") {
		t.Errorf("nothing is shared here, so nothing should be retained:\n%s", out)
	}
}

func TestGitSyncRecreate_UnknownRepositoryIsRefused(t *testing.T) {
	stub := &grafanaStub{repositories: healthyRepositories, connections: healthyConnections, dashboards: noDashboards}
	out, err := runGitSync(t, stub, "gitsync", "recreate", "--repository", "nope", "--confirm")
	if err == nil {
		t.Fatalf("expected an error\n%s", out)
	}
	if len(stub.deleted) != 0 {
		t.Fatalf("nothing should have been deleted: %v", stub.deleted)
	}
}

func TestGitSyncStatus_JSONEmitsEventsPlusSummary(t *testing.T) {
	stub := &grafanaStub{repositories: healthyRepositories, connections: healthyConnections, dashboards: noDashboards}
	out, err := runGitSync(t, stub, "--json", "gitsync", "status")
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, out)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 2 resource events plus a summary, got %d:\n%s", len(lines), out)
	}
	if !strings.Contains(lines[2], `"name":"summary"`) {
		t.Fatalf("last line should be the summary: %s", lines[2])
	}
}
