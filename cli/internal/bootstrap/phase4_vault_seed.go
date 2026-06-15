package bootstrap

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/jdwlabs/platform/internal/prompt"
	"github.com/jdwlabs/platform/internal/vault"
)

// client returns a lazily-built vault client, auto-forwarded and auto-tokened
// by the resolver on first use.
func (p *VaultSeedPhase) client(ctx context.Context) (*vault.Client, error) {
	if p.c == nil {
		var err error
		p.c, err = p.resolver.NewClient(ctx, "")
		if err != nil {
			return nil, err
		}
	}
	return p.c, nil
}

type seedField struct {
	Name     string
	EnvVar   string
	Secret   bool
	Optional bool // skip silently when env var not set
}

type seedSpec struct {
	Path   string
	Fields []seedField
}

// staticSeedSpecs holds platform-wide kv paths. Field names must match the
// `property:` keys in each service's ExternalSecret. Tenant-scoped paths are
// added by NewVaultSeedPhase based on the discovered tenant list.
var staticSeedSpecs = map[string]seedSpec{
	"porkbun": {Path: "porkbun", Fields: []seedField{
		{"api-key", "PLATFORMCTL_PORKBUN_API_KEY", true, false},
		{"secret-key", "PLATFORMCTL_PORKBUN_SECRET_KEY", true, false},
	}},
	"grafana": {Path: "grafana", Fields: []seedField{
		{"admin-user", "PLATFORMCTL_GRAFANA_ADMIN_USER", false, false},
		{"admin-password", "PLATFORMCTL_GRAFANA_ADMIN_PASSWORD", true, false},
	}},
	"longhorn": {Path: "longhorn", Fields: []seedField{
		{"htpasswd_string", "PLATFORMCTL_LONGHORN_HTPASSWD", true, false},
	}},
	"alertmanager": {Path: "alertmanager", Fields: []seedField{
		{"discord_webhook_url", "PLATFORMCTL_ALERTMANAGER_DISCORD_WEBHOOK", true, false},
	}},
	"usersrole": {Path: "usersrole", Fields: []seedField{
		{"jwt_key_non", "PLATFORMCTL_USERSROLE_JWT_KEY_NON", true, false},
		{"jwt_key_prd", "PLATFORMCTL_USERSROLE_JWT_KEY_PRD", true, false},
	}},
	"argocd-dex": {Path: "argocd-dex", Fields: []seedField{
		{"admin-password-hash", "PLATFORMCTL_ARGOCD_DEX_ADMIN_PASSWORD_HASH", true, false},
		{"headlamp-client-secret", "PLATFORMCTL_ARGOCD_DEX_HEADLAMP_CLIENT_SECRET", true, false},
		{"github-client-id", "PLATFORMCTL_ARGOCD_DEX_GITHUB_CLIENT_ID", false, false},
		{"github-client-secret", "PLATFORMCTL_ARGOCD_DEX_GITHUB_CLIENT_SECRET", true, false},
	}},
}

// VaultSeedPhase writes kv secrets for all platform and tenant paths.
type VaultSeedPhase struct {
	resolver       *VaultAddrResolver
	c              *vault.Client // lazily built by client()
	nonInteractive bool
	mount          string
	tenants        []string
	selected       []string // if non-empty, run only these spec keys
	onEvent        func(status, msg string)
}

func NewVaultSeedPhase(resolver *VaultAddrResolver, nonInteractive bool, mount string, tenants, selected []string) *VaultSeedPhase {
	return &VaultSeedPhase{resolver: resolver, nonInteractive: nonInteractive, mount: mount, tenants: tenants, selected: selected}
}

func (p *VaultSeedPhase) SetOnEvent(f func(status, msg string)) { p.onEvent = f }

func (p *VaultSeedPhase) warn(msg string) {
	if p.onEvent != nil {
		p.onEvent("progressing", "warn: "+msg)
	} else {
		fmt.Fprintln(os.Stderr, "warn:", msg)
	}
}

func (p *VaultSeedPhase) Name() string { return "vault-seed" }
func (p *VaultSeedPhase) Number() int  { return 4 }

func (p *VaultSeedPhase) Detect(ctx context.Context) (State, error) {
	c, err := p.client(ctx)
	if err != nil {
		return StateUnknown, err
	}
	if _, err := c.GetKV(ctx, p.mount, "porkbun"); err != nil {
		return StateNotStarted, nil
	}
	// Platform paths seeded. Warn about any tenant paths that are missing so
	// the operator knows to run `bootstrap seed <tenant>-github-app` etc.
	for _, t := range p.tenants {
		path := t + "-github-app"
		if _, err := c.GetKV(ctx, p.mount, path); err != nil {
			p.warn(fmt.Sprintf("kv/%s/%s missing — run: platformctl bootstrap seed %s", p.mount, path, path))
		}
	}
	return StateAlreadyDone, nil
}

func (p *VaultSeedPhase) Apply(ctx context.Context) error {
	c, err := p.client(ctx)
	if err != nil {
		return err
	}
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
			if v != "" {
				values[f.Name] = v
			}
		}
		if len(values) == 0 {
			continue // all optional fields absent — skip this path
		}
		if err := c.PutKV(ctx, p.mount, spec.Path, values); err != nil {
			return fmt.Errorf("put kv %s: %w", spec.Path, err)
		}
	}
	return nil
}

func (p *VaultSeedPhase) Verify(ctx context.Context) error {
	c, err := p.client(ctx)
	if err != nil {
		return err
	}
	if _, err := c.GetKV(ctx, p.mount, "porkbun"); err != nil {
		return fmt.Errorf("vault-seed verify: kv/%s/porkbun not seeded: %w", p.mount, err)
	}
	return nil
}

func (p *VaultSeedPhase) buildSpecs() map[string]seedSpec {
	out := map[string]seedSpec{}
	for k, v := range staticSeedSpecs {
		out[k] = v
	}
	for _, t := range p.tenants {
		u := toEnvKey(t)
		out[t+"-github-app"] = seedSpec{Path: t + "-github-app", Fields: []seedField{
			{"github_app_id", "PLATFORMCTL_" + u + "_GITHUB_APP_ID", false, false},
			{"github_app_installation_id", "PLATFORMCTL_" + u + "_GITHUB_INSTALLATION_ID", false, false},
			{"github_app_private_key", "PLATFORMCTL_" + u + "_GITHUB_PRIVATE_KEY", true, false},
		}}
		// ai-keys and discord are optional — not all tenants deploy these services.
		out[t+"-ai-keys"] = seedSpec{Path: t + "-ai-keys", Fields: []seedField{
			{"openai_api_key", "PLATFORMCTL_" + u + "_OPENAI_API_KEY", true, true},
			{"anthropic_api_key", "PLATFORMCTL_" + u + "_ANTHROPIC_API_KEY", true, true},
			{"openrouter_api_key", "PLATFORMCTL_" + u + "_OPENROUTER_API_KEY", true, true},
			{"nvidia_api_key", "PLATFORMCTL_" + u + "_NVIDIA_API_KEY", true, true},
			{"htpasswd_string", "PLATFORMCTL_" + u + "_OPENCLAW_HTPASSWD", true, true},
			{"gateway_password", "PLATFORMCTL_" + u + "_OPENCLAW_GATEWAY_PASSWORD", true, true},
		}}
		out[t+"-discord-bot-token"] = seedSpec{Path: t + "-discord-bot-token", Fields: []seedField{
			{"token", "PLATFORMCTL_" + u + "_DISCORD_BOT_TOKEN", true, true},
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
	if f.Optional && os.Getenv(f.EnvVar) == "" {
		return "", nil
	}
	label := fmt.Sprintf("[%s] %s", specName, f.Name)
	if f.Secret {
		return prompt.Secret(label, f.EnvVar, nonInteractive)
	}
	return prompt.String(label, f.EnvVar, nonInteractive)
}

func toEnvKey(s string) string {
	return strings.ToUpper(strings.ReplaceAll(s, "-", "_"))
}
