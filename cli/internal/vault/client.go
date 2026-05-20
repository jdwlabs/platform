package vault

import (
	"context"
	"errors"
	"fmt"

	vaultapi "github.com/hashicorp/vault/api"
)

// Client wraps the Vault HTTP API.
type Client struct {
	api *vaultapi.Client
}

// NewClient builds a Vault HTTP client. token may be empty if the caller sets
// it later via SetToken (e.g. after Init returns the root token).
func NewClient(addr, token string) (*Client, error) {
	cfg := vaultapi.DefaultConfig()
	cfg.Address = addr
	c, err := vaultapi.NewClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("vault client: %w", err)
	}
	if token != "" {
		c.SetToken(token)
	}
	return &Client{api: c}, nil
}

func (c *Client) SetToken(t string) { c.api.SetToken(t) }

// InitResult holds the unseal keys and root token from vault operator init.
type InitResult struct {
	Keys      []string `json:"keys"`
	KeysB64   []string `json:"keys_base64"`
	RootToken string   `json:"root_token"`
}

// Init performs vault operator init with the given shares/threshold.
func (c *Client) Init(ctx context.Context, shares, threshold int) (*InitResult, error) {
	resp, err := c.api.Sys().InitWithContext(ctx, &vaultapi.InitRequest{
		SecretShares:    shares,
		SecretThreshold: threshold,
	})
	if err != nil {
		return nil, fmt.Errorf("init: %w", err)
	}
	return &InitResult{Keys: resp.Keys, KeysB64: resp.KeysB64, RootToken: resp.RootToken}, nil
}

// Unseal applies keys one at a time until Vault reports unsealed.
func (c *Client) Unseal(ctx context.Context, keys []string) error {
	for _, k := range keys {
		st, err := c.api.Sys().UnsealWithContext(ctx, k)
		if err != nil {
			return fmt.Errorf("unseal: %w", err)
		}
		if !st.Sealed {
			return nil
		}
	}
	return errors.New("vault still sealed after providing all keys")
}

// IsInitialized returns whether vault operator init has been run.
func (c *Client) IsInitialized(ctx context.Context) (bool, error) {
	st, err := c.api.Sys().InitStatusWithContext(ctx)
	if err != nil {
		return false, fmt.Errorf("init status: %w", err)
	}
	return st, nil
}

// EnableKVv2 enables kv-v2 at mount. Idempotent: no-op if already enabled.
func (c *Client) EnableKVv2(ctx context.Context, mount string) error {
	mounts, err := c.api.Sys().ListMountsWithContext(ctx)
	if err != nil {
		return fmt.Errorf("list mounts: %w", err)
	}
	if _, exists := mounts[mount+"/"]; exists {
		return nil
	}
	return c.api.Sys().MountWithContext(ctx, mount, &vaultapi.MountInput{
		Type:    "kv",
		Options: map[string]string{"version": "2"},
	})
}

// PutKV writes a KV-v2 secret. Wraps the data envelope so callers don't have to.
func (c *Client) PutKV(ctx context.Context, mount, path string, data map[string]any) error {
	_, err := c.api.KVv2(mount).Put(ctx, path, data)
	return err
}

// GetKV reads a KV-v2 secret. Returns the data fields (not the KV metadata).
func (c *Client) GetKV(ctx context.Context, mount, path string) (map[string]any, error) {
	s, err := c.api.KVv2(mount).Get(ctx, path)
	if err != nil {
		return nil, err
	}
	return s.Data, nil
}

// EnableAuthMethod enables an auth backend at path with the given type
// (e.g. "kubernetes"). Idempotent: no-op if already enabled.
func (c *Client) EnableAuthMethod(ctx context.Context, path, kind string) error {
	auths, err := c.api.Sys().ListAuthWithContext(ctx)
	if err != nil {
		return fmt.Errorf("list auth: %w", err)
	}
	if _, exists := auths[path+"/"]; exists {
		return nil
	}
	return c.api.Sys().EnableAuthWithOptionsWithContext(ctx, path, &vaultapi.EnableAuthOptions{Type: kind})
}

// EnableSecretsEngine enables a secrets engine at mount with the given type
// (e.g. "database"). Idempotent: no-op if already mounted.
func (c *Client) EnableSecretsEngine(ctx context.Context, mount, kind string) error {
	mounts, err := c.api.Sys().ListMountsWithContext(ctx)
	if err != nil {
		return fmt.Errorf("list mounts: %w", err)
	}
	if _, exists := mounts[mount+"/"]; exists {
		return nil
	}
	return c.api.Sys().MountWithContext(ctx, mount, &vaultapi.MountInput{Type: kind})
}

// PutPolicy writes (or replaces) an ACL policy by name.
func (c *Client) PutPolicy(ctx context.Context, name, policyHCL string) error {
	return c.api.Sys().PutPolicyWithContext(ctx, name, policyHCL)
}

// Write performs a Logical().Write at the given path. Use for endpoints
// without dedicated SDK methods (e.g. auth/kubernetes/config, auth role writes).
func (c *Client) Write(ctx context.Context, path string, data map[string]any) error {
	_, err := c.api.Logical().WriteWithContext(ctx, path, data)
	return err
}

// Builder produces a Client with a per-call token. Lets phases inject a test
// server without exposing Client internals.
type Builder interface {
	New(ctx context.Context, token string) (*Client, error)
}

type addrBuilder struct{ addr string }

// NewBuilder returns a Builder that connects to addr with the given token.
func NewBuilder(addr string) Builder { return &addrBuilder{addr: addr} }

func (b *addrBuilder) New(_ context.Context, token string) (*Client, error) {
	return NewClient(b.addr, token)
}
