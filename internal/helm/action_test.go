package helm

import (
	"context"
	"testing"
)

func TestFakeRunner_RecordsCalls(t *testing.T) {
	f := &FakeRunner{}
	if err := f.UpgradeInstall(context.Background(), "platform-argo-cd", "argo/argo-cd", nil, "argocd"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(f.Calls) != 1 || f.Calls[0] != "platform-argo-cd/argo/argo-cd" {
		t.Fatalf("calls: %v", f.Calls)
	}
}

func TestFakeRunner_ReturnsErr(t *testing.T) {
	f := &FakeRunner{Err: context.DeadlineExceeded}
	err := f.UpgradeInstall(context.Background(), "demo", "argo/argo-cd", nil, "argocd")
	if err == nil {
		t.Fatal("expected error")
	}
}
