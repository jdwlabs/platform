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
	c              *vault.Client
	nonInteractive bool
	mount          string
}

func NewBackupsInitPhase(c *vault.Client, nonInteractive bool, mount string) *BackupsInitPhase {
	return &BackupsInitPhase{c: c, nonInteractive: nonInteractive, mount: mount}
}

func (p *BackupsInitPhase) Name() string { return "backups-init" }
func (p *BackupsInitPhase) Number() int  { return 5 }

func (p *BackupsInitPhase) Detect(ctx context.Context) (State, error) {
	if _, err := p.c.GetKV(ctx, p.mount, "rclone-gdrive"); err == nil {
		return StateAlreadyDone, nil
	}
	return StateNotStarted, nil
}

func (p *BackupsInitPhase) Apply(ctx context.Context) error {
	block, err := prompt.Secret(
		"Paste the rclone config block for [gdrive] (obtain via: rclone authorize \"drive\")",
		"PLATFORMCTL_RCLONE_GDRIVE_BLOCK", p.nonInteractive,
	)
	if err != nil {
		return err
	}
	if err := validateRcloneBlock(block); err != nil {
		return fmt.Errorf("rclone block invalid: %w", err)
	}
	return p.c.PutKV(ctx, p.mount, "rclone-gdrive", map[string]any{"rclone.conf": block})
}

func (p *BackupsInitPhase) Verify(ctx context.Context) error {
	got, err := p.c.GetKV(ctx, p.mount, "rclone-gdrive")
	if err != nil {
		return err
	}
	conf, ok := got["rclone.conf"].(string)
	if !ok || conf == "" {
		return fmt.Errorf("rclone-gdrive missing rclone.conf field")
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
