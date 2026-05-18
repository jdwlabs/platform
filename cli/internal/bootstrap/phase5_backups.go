package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/jdwlabs/platform/internal/prompt"
	"github.com/jdwlabs/platform/internal/vault"
)

var rcloneTokenRE = regexp.MustCompile(`(?m)^token\s*=\s*(.+)$`)

// BackupsInitPhase captures the rclone gdrive OAuth token from the operator
// and writes it to Vault so the postgres-backup CronJob can use it.
type BackupsInitPhase struct {
	resolver       *VaultAddrResolver
	c              *vault.Client // lazily built
	nonInteractive bool
	mount          string
}

func NewBackupsInitPhase(resolver *VaultAddrResolver, nonInteractive bool, mount string) *BackupsInitPhase {
	return &BackupsInitPhase{resolver: resolver, nonInteractive: nonInteractive, mount: mount}
}

func (p *BackupsInitPhase) client(ctx context.Context) (*vault.Client, error) {
	if p.c == nil {
		var err error
		p.c, err = p.resolver.NewClient(ctx, "")
		if err != nil {
			return nil, err
		}
	}
	return p.c, nil
}

func (p *BackupsInitPhase) Name() string { return "backups-init" }
func (p *BackupsInitPhase) Number() int  { return 5 }

func (p *BackupsInitPhase) Detect(ctx context.Context) (State, error) {
	c, err := p.client(ctx)
	if err != nil {
		return StateUnknown, err
	}
	if _, err := c.GetKV(ctx, p.mount, "rclone-gdrive"); err == nil {
		return StateAlreadyDone, nil
	}
	return StateNotStarted, nil
}

func (p *BackupsInitPhase) Apply(ctx context.Context) error {
	c, err := p.client(ctx)
	if err != nil {
		return err
	}
	block, err := prompt.Secret(
		"Paste the rclone config block for [gdrive] (obtain via: rclone authorize \"drive\")",
		"PLATFORMCTL_RCLONE_CONF", p.nonInteractive,
	)
	if err != nil {
		return err
	}
	if err := validateRcloneBlock(block); err != nil {
		return fmt.Errorf("rclone block invalid: %w", err)
	}
	return c.PutKV(ctx, p.mount, "rclone-gdrive", map[string]any{"rclone_conf": block})
}

func (p *BackupsInitPhase) Verify(ctx context.Context) error {
	c, err := p.client(ctx)
	if err != nil {
		return err
	}
	got, err := c.GetKV(ctx, p.mount, "rclone-gdrive")
	if err != nil {
		return err
	}
	conf, ok := got["rclone_conf"].(string)
	if !ok || conf == "" {
		return fmt.Errorf("rclone-gdrive missing rclone_conf field")
	}
	return validateRcloneBlock(conf)
}

func validateRcloneBlock(s string) error {
	if !strings.Contains(s, "[gdrive]") {
		return fmt.Errorf("missing [gdrive] section header")
	}
	m := rcloneTokenRE.FindStringSubmatch(s)
	if len(m) < 2 {
		return fmt.Errorf("missing token = {...} line")
	}
	var tok map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(m[1])), &tok); err != nil {
		return fmt.Errorf("token is not valid JSON: %w", err)
	}
	if _, ok := tok["access_token"]; !ok {
		return fmt.Errorf("token JSON missing access_token")
	}
	if _, ok := tok["refresh_token"]; !ok {
		return fmt.Errorf("token JSON missing refresh_token")
	}
	return nil
}
