package gitsync

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const repositoriesJSON = `{"items":[{
	"metadata":{"name":"platform-dashboards"},
	"spec":{"connection":{"name":"jdwlabs-platform-github"}},
	"status":{
		"health":{"healthy":false,"message":["branch \"main\" has protection rules that prevent direct pushes","the \"write\" workflow is not compatible with this branch"]},
		"sync":{"state":"error"}
	}}]}`

const connectionsJSON = `{"items":[{
	"metadata":{"name":"jdwlabs-platform-github"},
	"status":{"health":{"healthy":true},"sync":{"state":"success"}}}]}`

const dashboardsJSON = `{"items":[
	{"metadata":{"name":"owned","annotations":{"grafana.app/managerKind":"repo","grafana.app/managerId":"platform-dashboards"}}},
	{"metadata":{"name":"sidecar","annotations":{"grafana.app/managedBy":"classic-file-provisioning"}}},
	{"metadata":{"name":"other-repo","annotations":{"grafana.app/managerKind":"repo","grafana.app/managerId":"someone-else"}}},
	{"metadata":{"name":"hand-made","annotations":{}}}
]}`

func newTestServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

func TestList_ParsesHealthAndSyncState(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "admin" || pass != "s3cret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/repositories"):
			_, _ = w.Write([]byte(repositoriesJSON))
		case strings.HasSuffix(r.URL.Path, "/connections"):
			_, _ = w.Write([]byte(connectionsJSON))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	c := NewClient(srv.URL, "admin", "s3cret", DefaultNamespace)

	repos, err := c.List(context.Background(), KindRepository)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(repos) != 1 {
		t.Fatalf("got %d repositories, want 1", len(repos))
	}
	r := repos[0]
	if r.Name != "platform-dashboards" || r.Healthy || !r.HealthKnown {
		t.Fatalf("repository parsed wrong: %+v", r)
	}
	if r.SyncState != "error" {
		t.Errorf("sync state = %q, want error", r.SyncState)
	}
	if r.ConnectionRef != "jdwlabs-platform-github" {
		t.Errorf("connection ref = %q", r.ConnectionRef)
	}
	// A multi-line health message must survive as one readable string, because
	// the second line is the part that names the real cause.
	if !strings.Contains(r.HealthMessage, "protection rules") ||
		!strings.Contains(r.HealthMessage, `"write" workflow`) {
		t.Errorf("health message lost detail: %q", r.HealthMessage)
	}

	conns, err := c.List(context.Background(), KindConnection)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !conns[0].Healthy || !conns[0].HealthKnown {
		t.Fatalf("connection parsed wrong: %+v", conns[0])
	}
}

func TestList_MissingHealthBlockIsUnknownNotHealthy(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"items":[{"metadata":{"name":"platform-dashboards"},"status":{}}]}`))
	})
	c := NewClient(srv.URL, "u", "p", DefaultNamespace)
	res, err := c.List(context.Background(), KindRepository)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res[0].Healthy {
		t.Fatal("a resource with no health block must never read as healthy")
	}
	if res[0].HealthKnown {
		t.Fatal("a resource with no health block must report its health as unknown")
	}
	if !strings.Contains(res[0].HealthMessage, "status.health") {
		t.Fatalf("the absent field should be named: %q", res[0].HealthMessage)
	}
}

func TestList_HealthErrorFieldIsReported(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"items":[{"metadata":{"name":"c"},"status":{"health":{"healthy":false,` +
			`"error":"GitHub App lacks required 'webhooks' permission: requires 'write', has ''"}}}]}`))
	})
	c := NewClient(srv.URL, "u", "p", DefaultNamespace)
	res, err := c.List(context.Background(), KindConnection)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(res[0].HealthMessage, "webhooks") {
		t.Fatalf("health error must be surfaced: %q", res[0].HealthMessage)
	}
}

func TestList_EmptyCollectionIsNotAnError(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"items":[]}`))
	})
	c := NewClient(srv.URL, "u", "p", DefaultNamespace)
	res, err := c.List(context.Background(), KindRepository)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res) != 0 {
		t.Fatalf("got %d resources, want 0", len(res))
	}
}

func TestList_UnauthorizedIsTranslated(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"kind":"Status","message":"Unauthorized"}`))
	})
	c := NewClient(srv.URL, "u", "wrong", DefaultNamespace)
	_, err := c.List(context.Background(), KindRepository)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "credentials") {
		t.Fatalf("401 should be translated into a credential problem, got: %v", err)
	}
}

func TestList_UnknownKindIsRejectedBeforeAnyRequest(t *testing.T) {
	c := NewClient("http://127.0.0.1:1", "u", "p", DefaultNamespace)
	if _, err := c.List(context.Background(), "dashboard"); err == nil {
		t.Fatal("expected an error for an unsupported kind")
	}
}

func TestDelete_AbsentResourceIsANoOp(t *testing.T) {
	var method, path string
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		w.WriteHeader(http.StatusNotFound)
	})
	c := NewClient(srv.URL, "u", "p", DefaultNamespace)
	if err := c.Delete(context.Background(), KindRepository, "platform-dashboards"); err != nil {
		t.Fatalf("deleting an absent resource must be a no-op, got: %v", err)
	}
	if method != http.MethodDelete {
		t.Errorf("method = %s", method)
	}
	if !strings.HasSuffix(path, "/repositories/platform-dashboards") {
		t.Errorf("path = %s", path)
	}
}

func TestDelete_ServerErrorSurfacesTheBody(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"message":"finalizer still running"}`))
	})
	c := NewClient(srv.URL, "u", "p", DefaultNamespace)
	err := c.Delete(context.Background(), KindConnection, "jdwlabs-platform-github")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "finalizer") {
		t.Fatalf("the response body carries the reason and must be kept: %v", err)
	}
}

func TestDashboardsOwnedBy(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "dashboard.grafana.app") {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(dashboardsJSON))
	})
	c := NewClient(srv.URL, "u", "p", DefaultNamespace)
	owned, err := c.DashboardsOwnedBy(context.Background(), "platform-dashboards")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(owned) != 1 || owned[0] != "owned" {
		t.Fatalf("owned = %v, want [owned]", owned)
	}
}

func TestDashboardsOwnedBy_UnreachableApiIsAnErrorNotZero(t *testing.T) {
	// A delete guard that reads "no dashboards" from a failed request would be
	// worse than no guard at all.
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	c := NewClient(srv.URL, "u", "p", DefaultNamespace)
	if _, err := c.DashboardsOwnedBy(context.Background(), "platform-dashboards"); err == nil {
		t.Fatal("expected an error, got nil")
	}
}

func TestRepositoriesUsingConnection(t *testing.T) {
	repos := []Resource{
		{Kind: KindRepository, Name: "platform-dashboards", ConnectionRef: "jdwlabs-platform-github"},
		{Kind: KindRepository, Name: "other", ConnectionRef: "another-connection"},
	}
	got := RepositoriesUsingConnection(repos, "jdwlabs-platform-github")
	if len(got) != 1 || got[0] != "platform-dashboards" {
		t.Fatalf("got %v, want [platform-dashboards]", got)
	}
	if got := RepositoriesUsingConnection(repos, "unused"); len(got) != 0 {
		t.Fatalf("got %v, want none", got)
	}
}
