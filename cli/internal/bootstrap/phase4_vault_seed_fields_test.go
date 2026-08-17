package bootstrap

import (
	"context"
	"strings"
	"testing"
)

func TestValidateSeedSelection_UnknownSpecKeyNamesTheValidSet(t *testing.T) {
	err := ValidateSeedSelection(nil, []string{"holmesgpt"}, nil)
	if err == nil {
		t.Fatal("an unknown spec key must be an error, not an empty seed")
	}
	if !strings.Contains(err.Error(), "holmes") {
		t.Fatalf("error should list the valid keys: %v", err)
	}
}

func TestValidateSeedSelection_KnownKeysPass(t *testing.T) {
	if err := ValidateSeedSelection([]string{"demo"}, []string{"holmes", "demo-github-app"}, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateSeedSelection_UnknownFieldNamesTheSpecsFields(t *testing.T) {
	err := ValidateSeedSelection(nil, []string{"holmes"}, []string{"webhook-token"})
	if err == nil {
		t.Fatal("expected an error for a misspelled field")
	}
	if !strings.Contains(err.Error(), "webhook_token") {
		t.Fatalf("error should list the spec's real fields: %v", err)
	}
}

func TestValidateSeedSelection_FieldRequiresExactlyOneSpec(t *testing.T) {
	for _, selected := range [][]string{nil, {"holmes", "litellm"}} {
		err := ValidateSeedSelection(nil, selected, []string{"webhook_token"})
		if err == nil {
			t.Fatalf("--field with %d specs must be rejected", len(selected))
		}
		if !strings.Contains(err.Error(), "exactly one spec") {
			t.Fatalf("unexpected error: %v", err)
		}
	}
}

func TestSeedFieldNames(t *testing.T) {
	names, err := SeedFieldNames(nil, "holmes")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !contains(names, "webhook_token") || !contains(names, "jira_api_token") {
		t.Fatalf("holmes fields = %v", names)
	}
	if _, err := SeedFieldNames(nil, "nope"); err == nil {
		t.Fatal("expected an error for an unknown spec")
	}
}

func TestApply_SingleFieldWritesThatFieldAndKeepsTheRest(t *testing.T) {
	srv, c := mockVaultKV(t)
	ctx := context.Background()

	// A path already holding the credentials a full re-seed would demand.
	if err := c.PutKV(ctx, "secret", "holmes", map[string]any{
		"discord_webhook_url": "https://discord/original",
		"jira_url":            "https://jira",
		"jira_email":          "a@b.c",
		"jira_api_token":      "jira-token",
		"github_token":        "gh-token",
	}); err != nil {
		t.Fatalf("seed precondition: %v", err)
	}

	t.Setenv("PLATFORMCTL_HOLMES_WEBHOOK_TOKEN", "relay-bearer")
	p := NewVaultSeedPhase(NewVaultAddrResolver(srv.URL, nil, nil), true, "secret", nil, []string{"holmes"})
	p.SelectFields([]string{"webhook_token"})

	var events []string
	p.SetOnEvent(func(status, msg string) { events = append(events, status+" "+msg) })

	if err := p.Apply(ctx); err != nil {
		t.Fatalf("apply: %v", err)
	}

	got, err := c.GetKV(ctx, "secret", "holmes")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got["webhook_token"] != "relay-bearer" {
		t.Errorf("webhook_token = %v", got["webhook_token"])
	}
	for _, key := range []string{"discord_webhook_url", "jira_url", "jira_email", "jira_api_token", "github_token"} {
		if got[key] == nil {
			t.Errorf("%s was wiped by a single-field seed", key)
		}
	}
	if got["discord_webhook_url"] != "https://discord/original" {
		t.Errorf("an untouched field changed: %v", got["discord_webhook_url"])
	}
	if len(events) != 1 || !strings.Contains(events[0], "webhook_token created") {
		t.Fatalf("expected one created event naming the field, got %v", events)
	}
}

func TestApply_SingleFieldOverwriteIsReportedAsUpdated(t *testing.T) {
	srv, c := mockVaultKV(t)
	ctx := context.Background()
	if err := c.PutKV(ctx, "secret", "holmes", map[string]any{"webhook_token": "old"}); err != nil {
		t.Fatalf("seed precondition: %v", err)
	}

	t.Setenv("PLATFORMCTL_HOLMES_WEBHOOK_TOKEN", "new")
	p := NewVaultSeedPhase(NewVaultAddrResolver(srv.URL, nil, nil), true, "secret", nil, []string{"holmes"})
	p.SelectFields([]string{"webhook_token"})
	var events []string
	p.SetOnEvent(func(status, msg string) { events = append(events, msg) })

	if err := p.Apply(ctx); err != nil {
		t.Fatalf("apply: %v", err)
	}
	got, err := c.GetKV(ctx, "secret", "holmes")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	// Seeding an existing field must actually write it — a no-op here is the
	// failure mode this path exists to remove.
	if got["webhook_token"] != "new" {
		t.Fatalf("webhook_token = %v, want new", got["webhook_token"])
	}
	if len(events) != 1 || !strings.Contains(events[0], "updated") {
		t.Fatalf("an overwrite must be reported as updated, got %v", events)
	}
}

func TestApply_SingleFieldSelectsAnOptionalFieldToo(t *testing.T) {
	srv, c := mockVaultKV(t)
	ctx := context.Background()
	t.Setenv("PLATFORMCTL_HOLMES_LITELLM_KEY", "sk-1234")

	p := NewVaultSeedPhase(NewVaultAddrResolver(srv.URL, nil, nil), true, "secret", nil, []string{"holmes"})
	p.SelectFields([]string{"litellm_key"})
	if err := p.Apply(ctx); err != nil {
		t.Fatalf("apply: %v", err)
	}
	got, err := c.GetKV(ctx, "secret", "holmes")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got["litellm_key"] != "sk-1234" {
		t.Fatalf("litellm_key = %v", got["litellm_key"])
	}
}

func TestApply_UnknownSpecKeyFailsInsteadOfReportingSuccess(t *testing.T) {
	srv, _ := mockVaultKV(t)
	p := NewVaultSeedPhase(NewVaultAddrResolver(srv.URL, nil, nil), true, "secret", nil, []string{"grafana-git-sync"})
	if err := p.Apply(context.Background()); err == nil {
		t.Fatal("a mistyped spec key must fail, not write nothing and succeed")
	}
}

func TestSeedSpec_RcloneGdriveRotatableByField(t *testing.T) {
	if err := ValidateSeedSelection(nil, []string{"rclone-gdrive"}, []string{"rclone_conf"}); err != nil {
		t.Fatalf("rotating the Drive config must be a supported seed selection: %v", err)
	}
}

// --field overrides Optional, so the rotation path must still be gated by the
// same block validation the first-time capture in phase 5 applies.
func TestValidateSeedValue_RcloneConfIsValidated(t *testing.T) {
	if err := validateSeedValue("rclone-gdrive", "rclone_conf", "[gdrive]\ntype = drive\n"); err == nil {
		t.Fatal("expected a block with no token to be rejected")
	}
	if err := validateSeedValue("rclone-gdrive", "rclone_conf", validRcloneBlock); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
}

func TestValidateSeedValue_UnrelatedFieldsUnaffected(t *testing.T) {
	if err := validateSeedValue("porkbun", "api-key", "anything"); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
}
