package cli

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes"
	kubefake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

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
		// What tenant-envelope leaves behind: the only durable record of which
		// tenant's team is granted on which git sync folder. jdwlabs claims one,
		// dotablaze-tech is modelled without one so both branches are covered.
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "tenant-jdwlabs-grafana-observability",
				Namespace: "monitoring",
				Labels:    map[string]string{"platform.jdwlabs.io/tenant": "jdwlabs"},
			},
			Data: map[string]string{"gitsync-folder": "jdwlabs-dashboards"},
		},
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "tenant-dotablaze-tech-grafana-observability",
				Namespace: "monitoring",
				Labels:    map[string]string{"platform.jdwlabs.io/tenant": "dotablaze-tech"},
			},
			Data: map[string]string{},
		},
	}
}

func argoApp(name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "Application",
		"metadata":   map[string]interface{}{"name": name, "namespace": "argocd"},
	}}
}

// refreshedApps names the Applications carrying a hard-refresh annotation, and
// syncedApps those carrying a sync operation. They are deliberately separate:
// a refresh re-runs the comparison, and an Application that compares Synced
// runs no hooks — so asserting "a patch was sent" is exactly how a folder-RBAC
// hook that never executes passes a test suite.
func refreshedApps(t *testing.T, dc dynamic.Interface) []string {
	t.Helper()
	var out []string
	for _, app := range listApps(t, dc) {
		if app.GetAnnotations()["argocd.argoproj.io/refresh"] != "" {
			out = append(out, app.GetName())
		}
	}
	sort.Strings(out)
	return out
}

func syncedApps(t *testing.T, dc dynamic.Interface) []string {
	t.Helper()
	var out []string
	for _, app := range listApps(t, dc) {
		if _, found, _ := unstructured.NestedMap(app.Object, "operation", "sync"); found {
			out = append(out, app.GetName())
		}
	}
	sort.Strings(out)
	return out
}

func listApps(t *testing.T, dc dynamic.Interface) []unstructured.Unstructured {
	t.Helper()
	gvr := schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}
	list, err := dc.Resource(gvr).Namespace("argocd").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("listing applications: %v", err)
	}
	return list.Items
}

func runGitSync(t *testing.T, stub *grafanaStub, args ...string) (string, error) {
	t.Helper()
	out, _, err := runGitSyncWithCluster(t, stub, args...)
	return out, err
}

func runGitSyncWithCluster(t *testing.T, stub *grafanaStub, args ...string) (string, dynamic.Interface, error) {
	t.Helper()
	return runGitSyncWithKube(t, stub, k8s.NewFake(grafanaKubeObjects()...), args...)
}

func runGitSyncWithKube(t *testing.T, stub *grafanaStub, kc kubernetes.Interface, args ...string) (string, dynamic.Interface, error) {
	t.Helper()
	srv := stub.start(t)
	dc := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}: "ApplicationList",
		},
		argoApp("platform-grafana"), argoApp("governance-jdwlabs"),
		argoApp("governance-dotablaze-tech"), argoApp("governance-jdwillmsen"),
	)
	root := NewRootForTest(kc, dc)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(append(args, "--grafana-addr", srv.URL))
	err := root.Execute()
	return out.String(), dc, err
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

func TestGitSyncDelete_SharedConnectionRefusalNamesEveryRepository(t *testing.T) {
	stub := &grafanaStub{repositories: sharedConnectionRepositories, connections: healthyConnections, dashboards: noDashboards}
	out, err := runGitSync(t, stub, "gitsync", "delete",
		"--kind", "connection", "--name", "jdwlabs-platform-github", "--confirm")
	if err == nil {
		t.Fatalf("three repositories reference this connection\n%s", out)
	}
	for _, name := range []string{"platform-dashboards", "jdwlabs-dashboards", "dotablaze-tech-dashboards"} {
		if !strings.Contains(out, name) {
			t.Errorf("the way out must name %s, since all three block the delete:\n%s", name, out)
		}
	}
	// The pre-shared-connection advice was "recreate does both in order",
	// which recreate no longer does — following it loops forever.
	if !strings.Contains(out, "--with-connection") {
		t.Errorf("the suggested command must be one that actually deletes the connection:\n%s", out)
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

func TestGitSyncRecreate_WithConnectionTakesEverySiblingThenTheConnection(t *testing.T) {
	stub := &grafanaStub{repositories: sharedConnectionRepositories, connections: healthyConnections, dashboards: noDashboards}
	out, err := runGitSync(t, stub, "gitsync", "recreate",
		"--repository", "jdwlabs-dashboards", "--with-connection", "--confirm")
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, out)
	}
	want := []string{
		"/repositories/jdwlabs-dashboards",
		"/repositories/dotablaze-tech-dashboards",
		"/repositories/platform-dashboards",
		"/connections/jdwlabs-platform-github",
	}
	if len(stub.deleted) != len(want) {
		t.Fatalf("changing the connection needs every repository off it first, got %v", stub.deleted)
	}
	// The connection last is the whole point: it cannot be deleted underneath
	// a repository that still references it.
	if !strings.HasSuffix(stub.deleted[len(stub.deleted)-1], "/connections/jdwlabs-platform-github") {
		t.Errorf("the connection must be deleted last: %v", stub.deleted)
	}
	for _, path := range want {
		found := false
		for _, got := range stub.deleted {
			if strings.HasSuffix(got, path) {
				found = true
			}
		}
		if !found {
			t.Errorf("%s was never deleted: %v", path, stub.deleted)
		}
	}
	if !strings.Contains(out, "cascade:") {
		t.Errorf("deleting repositories the operator did not name must be said out loud:\n%s", out)
	}
	if strings.Contains(out, "retained:") {
		t.Errorf("nothing is retained when the connection goes:\n%s", out)
	}
}

func TestGitSyncRecreate_SharedConnectionPointsAtTheWayToChangeIt(t *testing.T) {
	stub := &grafanaStub{repositories: sharedConnectionRepositories, connections: healthyConnections, dashboards: noDashboards}
	out, err := runGitSync(t, stub, "gitsync", "recreate", "--repository", "jdwlabs-dashboards", "--dry-run")
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, out)
	}
	if !strings.Contains(out, "--with-connection") {
		t.Errorf("a retained connection is exactly where the flag that changes it belongs:\n%s", out)
	}
}

func TestGitSyncRecreate_WithConnectionChecksSiblingsForOwnedDashboards(t *testing.T) {
	stub := &grafanaStub{repositories: sharedConnectionRepositories, connections: healthyConnections, dashboards: ownedDashboards}
	out, err := runGitSync(t, stub, "gitsync", "recreate",
		"--repository", "jdwlabs-dashboards", "--with-connection", "--confirm")
	if err == nil {
		t.Fatalf("the finalizer does not care which repository was named\n%s", out)
	}
	if !strings.Contains(out, "vault-overview") {
		t.Errorf("the refusal should name the dashboards at risk:\n%s", out)
	}
	if len(stub.deleted) != 0 {
		t.Fatalf("nothing should have been deleted: %v", stub.deleted)
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

func TestGitSyncDelete_TenantFolderRepositoryIsRefused(t *testing.T) {
	// Deleting it takes the folder with it, and the folder Grafana recreates
	// carries the inherited grants until the tenant envelope syncs — which this
	// command never asks for. Refusing is what stops the documented rollback
	// from quietly reopening the boundary.
	stub := &grafanaStub{repositories: sharedConnectionRepositories, connections: healthyConnections, dashboards: noDashboards}
	out, err := runGitSync(t, stub, "gitsync", "delete",
		"--kind", "repository", "--name", "jdwlabs-dashboards", "--confirm")
	if err == nil {
		t.Fatalf("a repository carrying tenant RBAC must not delete silently\n%s", out)
	}
	if !strings.Contains(out, "jdwlabs") || !strings.Contains(out, "governance-jdwlabs") {
		t.Errorf("the refusal must name the tenant and the app that re-grants it:\n%s", out)
	}
	if !strings.Contains(out, "--accept-open-folder") {
		t.Errorf("the override must be discoverable from the refusal:\n%s", out)
	}
	if len(stub.deleted) != 0 {
		t.Fatalf("nothing should have been deleted: %v", stub.deleted)
	}
}

func TestGitSyncDelete_UnclaimedRepositoryStillDeletes(t *testing.T) {
	stub := &grafanaStub{repositories: sharedConnectionRepositories, connections: healthyConnections, dashboards: noDashboards}
	out, err := runGitSync(t, stub, "gitsync", "delete",
		"--kind", "repository", "--name", "dotablaze-tech-dashboards", "--confirm")
	if err != nil {
		t.Fatalf("no tenant claims this folder, so nothing should block it: %v\n%s", err, out)
	}
	if len(stub.deleted) != 1 {
		t.Fatalf("deleted = %v", stub.deleted)
	}
}

func TestGitSyncDelete_AcceptOpenFolderProceedsAndSaysWhatIsOwed(t *testing.T) {
	stub := &grafanaStub{repositories: sharedConnectionRepositories, connections: healthyConnections, dashboards: noDashboards}
	out, err := runGitSync(t, stub, "gitsync", "delete",
		"--kind", "repository", "--name", "jdwlabs-dashboards", "--confirm", "--accept-open-folder")
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, out)
	}
	if len(stub.deleted) != 1 {
		t.Fatalf("deleted = %v", stub.deleted)
	}
	if !strings.Contains(out, "governance-jdwlabs") {
		t.Errorf("overriding the guard must still print the follow-up:\n%s", out)
	}
}

func TestGitSyncRecreate_ResyncsTheTenantEnvelopeThatOwnsTheFolder(t *testing.T) {
	stub := &grafanaStub{repositories: sharedConnectionRepositories, connections: healthyConnections, dashboards: noDashboards}
	out, dc, err := runGitSyncWithCluster(t, stub, "gitsync", "recreate",
		"--repository", "jdwlabs-dashboards", "--confirm")
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, out)
	}
	// The envelope needs a sync operation, not a refresh: nothing about
	// governance-jdwlabs changed, so it compares Synced and a refresh runs no
	// hooks — the folder would keep the inherited grants indefinitely while the
	// command reported success.
	if got := syncedApps(t, dc); strings.Join(got, " ") != "governance-jdwlabs" {
		t.Errorf("synced %v, want governance-jdwlabs\n%s", got, out)
	}
	if got := refreshedApps(t, dc); strings.Join(got, " ") != "platform-grafana" {
		t.Errorf("refreshed %v, want only platform-grafana — a refresh is not a sync\n%s", got, out)
	}
	if !strings.Contains(out, "envelopes: sync requested for governance-jdwlabs") {
		t.Errorf("the envelope sync must be reported, not inferred:\n%s", out)
	}
}

func TestGitSyncRecreate_UnclaimedRepositoryResyncsNoEnvelope(t *testing.T) {
	stub := &grafanaStub{repositories: sharedConnectionRepositories, connections: healthyConnections, dashboards: noDashboards}
	out, dc, err := runGitSyncWithCluster(t, stub, "gitsync", "recreate",
		"--repository", "dotablaze-tech-dashboards", "--confirm")
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, out)
	}
	if got := syncedApps(t, dc); len(got) != 0 {
		t.Errorf("no tenant claims this folder, so no envelope should be synced: %v\n%s", got, out)
	}
	if got := refreshedApps(t, dc); strings.Join(got, " ") != "platform-grafana" {
		t.Errorf("refreshed %v, want only platform-grafana\n%s", got, out)
	}
	if !strings.Contains(out, `envelopes: "none needed`) {
		t.Errorf("a definitive nothing beats an absent line:\n%s", out)
	}
}

func TestGitSyncRecreate_DryRunNamesTheEnvelopeItWouldResync(t *testing.T) {
	stub := &grafanaStub{repositories: sharedConnectionRepositories, connections: healthyConnections, dashboards: noDashboards}
	out, dc, err := runGitSyncWithCluster(t, stub, "gitsync", "recreate",
		"--repository", "jdwlabs-dashboards", "--dry-run")
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, out)
	}
	if !strings.Contains(out, "governance-jdwlabs") {
		t.Errorf("a preview that hides the RBAC consequence is not a preview:\n%s", out)
	}
	if got := refreshedApps(t, dc); len(got) != 0 {
		t.Fatalf("a dry run must mutate nothing, refreshed %v", got)
	}
	if got := syncedApps(t, dc); len(got) != 0 {
		t.Fatalf("a dry run must mutate nothing, synced %v", got)
	}
}

func TestGitSyncRecreate_WithConnectionResyncsEveryClaimedEnvelope(t *testing.T) {
	stub := &grafanaStub{repositories: sharedConnectionRepositories, connections: healthyConnections, dashboards: noDashboards}
	out, dc, err := runGitSyncWithCluster(t, stub, "gitsync", "recreate",
		"--repository", "platform-dashboards", "--with-connection", "--confirm")
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, out)
	}
	// The cascade deletes siblings nobody named, so it owes their envelopes too.
	if got := syncedApps(t, dc); strings.Join(got, " ") != "governance-jdwlabs" {
		t.Errorf("synced %v, want governance-jdwlabs\n%s", got, out)
	}
}

// unreadableClaims models the supported "reach Grafana directly, no usable
// kubeconfig" mode, where the ownership lookup is the thing that cannot run.
func unreadableClaims(t *testing.T) kubernetes.Interface {
	t.Helper()
	kc := kubefake.NewSimpleClientset(grafanaKubeObjects()...)
	kc.PrependReactor("list", "configmaps",
		func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, errors.New("configmaps is forbidden")
		})
	return kc
}

func TestGitSyncDelete_UnreadableClaimRefusesLikeAKnownOne(t *testing.T) {
	// Unknown must not be more permissive than known: treating an unreadable
	// cluster as "nothing is claimed" deletes exactly the repositories the
	// known-claim branch refuses.
	stub := &grafanaStub{repositories: sharedConnectionRepositories, connections: healthyConnections, dashboards: noDashboards}
	out, _, err := runGitSyncWithKube(t, stub, unreadableClaims(t), "gitsync", "delete",
		"--kind", "repository", "--name", "jdwlabs-dashboards", "--confirm")
	if err == nil {
		t.Fatalf("an unreadable claim must refuse\n%s", out)
	}
	if !strings.Contains(out, "--accept-open-folder") {
		t.Errorf("the same escape as a known claim must be offered:\n%s", out)
	}
	if len(stub.deleted) != 0 {
		t.Fatalf("nothing should have been deleted: %v", stub.deleted)
	}
}

func TestGitSyncDelete_UnreadableClaimAcceptedStillStatesTheObligation(t *testing.T) {
	stub := &grafanaStub{repositories: sharedConnectionRepositories, connections: healthyConnections, dashboards: noDashboards}
	out, _, err := runGitSyncWithKube(t, stub, unreadableClaims(t), "gitsync", "delete",
		"--kind", "repository", "--name", "jdwlabs-dashboards", "--confirm", "--accept-open-folder")
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, out)
	}
	if len(stub.deleted) != 1 {
		t.Fatalf("deleted = %v", stub.deleted)
	}
	if !strings.Contains(out, "could not be checked") || !strings.Contains(out, "argocd app sync") {
		t.Errorf("overriding must still say what may now be owed:\n%s", out)
	}
}

func TestGitSyncRecreate_UnreadableClaimRefusesRatherThanSyncingBlind(t *testing.T) {
	// recreate's claim to being the safe path is that it syncs the envelopes
	// itself, which it cannot do for claims it could not read.
	stub := &grafanaStub{repositories: sharedConnectionRepositories, connections: healthyConnections, dashboards: noDashboards}
	out, _, err := runGitSyncWithKube(t, stub, unreadableClaims(t), "gitsync", "recreate",
		"--repository", "jdwlabs-dashboards", "--confirm")
	if err == nil {
		t.Fatalf("an unreadable claim must refuse\n%s", out)
	}
	if len(stub.deleted) != 0 {
		t.Fatalf("nothing should have been deleted: %v", stub.deleted)
	}
}

func TestGitSyncRecreate_NoSyncIsNotHeldToTheClaimCheck(t *testing.T) {
	// --no-sync is already the operator saying they will drive the syncs.
	stub := &grafanaStub{repositories: sharedConnectionRepositories, connections: healthyConnections, dashboards: noDashboards}
	out, _, err := runGitSyncWithKube(t, stub, unreadableClaims(t), "gitsync", "recreate",
		"--repository", "jdwlabs-dashboards", "--confirm", "--no-sync")
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, out)
	}
	if len(stub.deleted) != 1 {
		t.Fatalf("deleted = %v", stub.deleted)
	}
}

func TestGitSyncRecreate_AcceptedUnreadableClaimDoesNotReportNoneNeeded(t *testing.T) {
	// An empty claim map for want of a read is not the same as no claimant,
	// and "none needed" off it is the same false all-clear as a refresh that
	// runs no hooks.
	stub := &grafanaStub{repositories: sharedConnectionRepositories, connections: healthyConnections, dashboards: noDashboards}
	out, dc, err := runGitSyncWithKube(t, stub, unreadableClaims(t), "gitsync", "recreate",
		"--repository", "jdwlabs-dashboards", "--confirm", "--accept-open-folder")
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, out)
	}
	if strings.Contains(out, "none needed") {
		t.Errorf("an unread claim must not be reported as nothing to do:\n%s", out)
	}
	if !strings.Contains(out, "not checked: configmaps is forbidden") ||
		!strings.Contains(out, "argocd app sync") {
		t.Errorf("the unsynced envelopes must be stated as owed:\n%s", out)
	}
	if got := syncedApps(t, dc); len(got) != 0 {
		t.Errorf("nothing was read, so nothing can be synced: %v", got)
	}
}

func TestGitSyncRecreate_EveryClaimantOfAFolderIsSynced(t *testing.T) {
	// Two tenants claiming one folder is rejected in git by
	// tools/check-gitsync-tenant-folders.py, but the cluster passes through it
	// while one drops the key and another adds it. Picking one arbitrarily is
	// how the wrong envelope gets synced; syncing both also reconciles the
	// stale claimant, whose ConfigMap loses the key on its own next sync.
	kc := kubefake.NewSimpleClientset(append(grafanaKubeObjects(),
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "tenant-jdwillmsen-grafana-observability",
				Namespace: "monitoring",
				Labels:    map[string]string{"platform.jdwlabs.io/tenant": "jdwillmsen"},
			},
			Data: map[string]string{"gitsync-folder": "jdwlabs-dashboards"},
		})...)
	stub := &grafanaStub{repositories: sharedConnectionRepositories, connections: healthyConnections, dashboards: noDashboards}
	out, dc, err := runGitSyncWithKube(t, stub, kc, "gitsync", "recreate",
		"--repository", "jdwlabs-dashboards", "--confirm")
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, out)
	}
	if got := syncedApps(t, dc); strings.Join(got, " ") != "governance-jdwillmsen governance-jdwlabs" {
		t.Errorf("synced %v, want both claimants\n%s", got, out)
	}
	if !strings.Contains(out, "governance-jdwillmsen") || !strings.Contains(out, "governance-jdwlabs") {
		t.Errorf("both claimants must be named:\n%s", out)
	}
}

func TestGitSyncDelete_AmbiguousClaimNamesEveryTenant(t *testing.T) {
	kc := kubefake.NewSimpleClientset(append(grafanaKubeObjects(),
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "tenant-jdwillmsen-grafana-observability",
				Namespace: "monitoring",
				Labels:    map[string]string{"platform.jdwlabs.io/tenant": "jdwillmsen"},
			},
			Data: map[string]string{"gitsync-folder": "jdwlabs-dashboards"},
		})...)
	stub := &grafanaStub{repositories: sharedConnectionRepositories, connections: healthyConnections, dashboards: noDashboards}
	out, _, err := runGitSyncWithKube(t, stub, kc, "gitsync", "delete",
		"--kind", "repository", "--name", "jdwlabs-dashboards", "--confirm")
	if err == nil {
		t.Fatalf("expected a refusal\n%s", out)
	}
	for _, tenant := range []string{"jdwlabs", "jdwillmsen"} {
		if !strings.Contains(out, governanceAppPrefix+tenant) {
			t.Errorf("the refusal must name %s, not one claimant arbitrarily:\n%s", tenant, out)
		}
	}
}
