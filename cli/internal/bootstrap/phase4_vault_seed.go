package bootstrap

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/jdwlabs/platform/internal/prompt"
	"github.com/jdwlabs/platform/internal/vault"
)

type seedField struct {
	Name   string
	EnvVar string
	Secret bool
}

type seedSpec struct {
	Path   string
	Fields []seedField
}

// staticSeedSpecs holds platform-wide kv paths. Tenant-scoped ones are added
// by NewVaultSeedPhase based on the discovered tenant list.
var staticSeedSpecs = map[string]seedSpec{
	"porkbun": {Path: "porkbun", Fields: []seedField{
		{"api_key", "PLATFORMCTL_PORKBUN_API_KEY", true},
		{"api_secret_key", "PLATFORMCTL_PORKBUN_API_SECRET_KEY", true},
	}},
	"grafana": {Path: "grafana", Fields: []seedField{
		{"admin_user", "PLATFORMCTL_GRAFANA_ADMIN_USER", false},
		{"admin_password", "PLATFORMCTL_GRAFANA_ADMIN_PASSWORD", true},
	}},
	"longhorn": {Path: "longhorn", Fields: []seedField{
		{"auth", "PLATFORMCTL_LONGHORN_AUTH", true},
	}},
	"alertmanager": {Path: "alertmanager", Fields: []seedField{
		{"slack_webhook", "PLATFORMCTL_ALERTMANAGER_SLACK_WEBHOOK", true},
	}},
	"usersrole": {Path: "usersrole", Fields: []seedField{
		{"jwt_secret", "PLATFORMCTL_USERSROLE_JWT_SECRET", true},
	}},
}

// VaultSeedPhase writes kv secrets for all platform and tenant paths.
type VaultSeedPhase struct {
	c              *vault.Client
	nonInteractive bool
	mount          string
	tenants        []string
	selected       []string // if non-empty, run only these spec keys
}

func NewVaultSeedPhase(c *vault.Client, nonInteractive bool, mount string, tenants, selected []string) *VaultSeedPhase {
	return &VaultSeedPhase{c: c, nonInteractive: nonInteractive, mount: mount, tenants: tenants, selected: selected}
}

func (p *VaultSeedPhase) Name() string { return "vault-seed" }
func (p *VaultSeedPhase) Number() int  { return 4 }

func (p *VaultSeedPhase) Detect(ctx context.Context) (State, error) {
	if _, err := p.c.GetKV(ctx, p.mount, "porkbun"); err == nil {
		return StateAlreadyDone, nil
	}
	return StateNotStarted, nil
}

func (p *VaultSeedPhase) Apply(ctx context.Context) error {
	specs := p.buildSpecs()
	keys := p.keysToRun(specs)
	sort.Strings(keys)
	for _, name := range keys {
		spec := specs[name]
		values := map[string]any{}
		for _, f := range spec.Fields {
			v, err := promptField(f, name, p.nonInteractive)
			if err != nil {
				return fmt.Errorf("seed %s/%s: %w", spec.Path, f.Name, err)
			}
			values[f.Name] = v
		}
		if err := p.c.PutKV(ctx, p.mount, spec.Path, values); err != nil {
			return fmt.Errorf("put kv %s: %w", spec.Path, err)
		}
	}
	return nil
}

func (p *VaultSeedPhase) Verify(ctx context.Context) error { return nil }

func (p *VaultSeedPhase) buildSpecs() map[string]seedSpec {
	out := map[string]seedSpec{}
	for k, v := range staticSeedSpecs {
		out[k] = v
	}
	for _, t := range p.tenants {
		u := toEnvKey(t)
		out[t+"-github-app"] = seedSpec{Path: t + "-github-app", Fields: []seedField{
			{"app_id", "PLATFORMCTL_" + u + "_GITHUB_APP_ID", false},
			{"installation_id", "PLATFORMCTL_" + u + "_GITHUB_INSTALLATION_ID", false},
			{"private_key", "PLATFORMCTL_" + u + "_GITHUB_PRIVATE_KEY", true},
		}}
		out[t+"-ai-keys"] = seedSpec{Path: t + "-ai-keys", Fields: []seedField{
			{"openai", "PLATFORMCTL_" + u + "_OPENAI_API_KEY", true},
			{"anthropic", "PLATFORMCTL_" + u + "_ANTHROPIC_API_KEY", true},
		}}
		out[t+"-discord-bot-token"] = seedSpec{Path: t + "-discord-bot-token", Fields: []seedField{
			{"token", "PLATFORMCTL_" + u + "_DISCORD_BOT_TOKEN", true},
		}}
	}
	return out
}

func (p *VaultSeedPhase) keysToRun(specs map[string]seedSpec) []string {
	if len(p.selected) > 0 {
		return p.selected
	}
	keys := make([]string, 0, len(specs))
	for k := range specs {
		keys = append(keys, k)
	}
	return keys
}

func promptField(f seedField, specName string, nonInteractive bool) (string, error) {
	label := fmt.Sprintf("[%s] %s", specName, f.Name)
	if f.Secret {
		return prompt.Secret(label, f.EnvVar, nonInteractive)
	}
	return prompt.String(label, f.EnvVar, nonInteractive)
}

func toEnvKey(s string) string {
	return strings.ToUpper(strings.ReplaceAll(s, "-", "_"))
}
