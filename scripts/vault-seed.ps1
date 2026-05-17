#Requires -Version 5.1
<#
.SYNOPSIS
    Seeds Vault KV paths required by all platform ExternalSecrets.

.DESCRIPTION
    Reads all secret values from environment variables (PLATFORMCTL_* prefix).
    Gets the Vault root token from the vault-init k8s secret automatically.
    Uses kubectl exec to write to Vault — no local vault CLI needed.

.EXAMPLE
    # Set env vars, then run:
    $env:PLATFORMCTL_PORKBUN_API_KEY = "pk1_..."
    .\scripts\vault-seed.ps1

    # Or pipe from a local secrets file (never commit that file):
    . .secrets\vault-seed-env.ps1
    .\scripts\vault-seed.ps1

.NOTES
    Required env vars — see REQUIRED VARIABLES section below.
    All vars with _SECRET or _KEY or _PASSWORD suffix are treated as sensitive
    and will not be echoed to the console.
#>

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

function Require-Env {
    param([string]$Name)
    $val = [System.Environment]::GetEnvironmentVariable($Name)
    if (-not $val) {
        throw "Required env var '$Name' is not set. See script header for full list."
    }
    return $val
}

function VaultPut {
    param(
        [string]$Path,
        [hashtable]$Fields
    )
    $ROOT_TOKEN = $script:ROOT_TOKEN
    $pairs = $Fields.GetEnumerator() | ForEach-Object {
        "$($_.Key)=$($_.Value)"
    }
    $cmd = "VAULT_TOKEN=$ROOT_TOKEN vault kv put kv/$Path $($pairs -join ' ')"
    $result = kubectl exec -n vault platform-vault-0 -- sh -c $cmd 2>&1
    if ($LASTEXITCODE -ne 0) {
        throw "vault kv put kv/$Path failed: $result"
    }
    Write-Host "  [ok] kv/$Path" -ForegroundColor Green
}

# ---------------------------------------------------------------------------
# Fetch root token from cluster
# ---------------------------------------------------------------------------

Write-Host "`nFetching Vault root token from cluster..." -ForegroundColor Cyan
$b64 = kubectl get secret vault-init -n vault -o "jsonpath={.data.vault-init\.json}" 2>&1
if ($LASTEXITCODE -ne 0) { throw "vault-init secret not found in namespace vault. Run bootstrap phase 3 first." }
$initJson = [System.Text.Encoding]::UTF8.GetString([System.Convert]::FromBase64String($b64)) | ConvertFrom-Json
$script:ROOT_TOKEN = $initJson.root_token
Write-Host "  Root token retrieved (length: $($script:ROOT_TOKEN.Length))" -ForegroundColor Green

# ---------------------------------------------------------------------------
# REQUIRED VARIABLES
# ---------------------------------------------------------------------------
# Set these before running. Sensitive vars can be in a local .secrets/ file
# (add .secrets/ to .gitignore — never commit actual values).
#
# PLATFORMCTL_PORKBUN_API_KEY
# PLATFORMCTL_PORKBUN_SECRET_KEY
# PLATFORMCTL_GRAFANA_ADMIN_USER
# PLATFORMCTL_GRAFANA_ADMIN_PASSWORD
# PLATFORMCTL_LONGHORN_HTPASSWD          (pre-computed htpasswd string)
# PLATFORMCTL_ALERTMANAGER_DISCORD_WEBHOOK
# PLATFORMCTL_DISCORD_BOT_TOKEN          (dotablaze-tech meowbot)
# PLATFORMCTL_USERSROLE_JWT_KEY_NON
# PLATFORMCTL_USERSROLE_JWT_KEY_PRD
# PLATFORMCTL_JDWLABS_ANTHROPIC_API_KEY
# PLATFORMCTL_JDWLABS_OPENAI_API_KEY
# PLATFORMCTL_JDWLABS_OPENROUTER_API_KEY
# PLATFORMCTL_JDWLABS_OPENCLAW_HTPASSWD  (pre-computed htpasswd for openclaw basic auth)
# PLATFORMCTL_JDWLABS_GITHUB_APP_ID
# PLATFORMCTL_JDWLABS_GITHUB_INSTALLATION_ID
# PLATFORMCTL_JDWLABS_GITHUB_PRIVATE_KEY  (PEM key, newlines as \n)
# PLATFORMCTL_DOTABLAZE_GITHUB_APP_ID
# PLATFORMCTL_DOTABLAZE_GITHUB_INSTALLATION_ID
# PLATFORMCTL_DOTABLAZE_GITHUB_PRIVATE_KEY
# PLATFORMCTL_RCLONE_CONF                 (full rclone.conf contents)
# ---------------------------------------------------------------------------

Write-Host "`nSeeding Vault KV paths..." -ForegroundColor Cyan

# kv/porkbun
VaultPut "porkbun" @{
    "api-key"    = Require-Env "PLATFORMCTL_PORKBUN_API_KEY"
    "secret-key" = Require-Env "PLATFORMCTL_PORKBUN_SECRET_KEY"
}

# kv/grafana
VaultPut "grafana" @{
    "admin-user"     = Require-Env "PLATFORMCTL_GRAFANA_ADMIN_USER"
    "admin-password" = Require-Env "PLATFORMCTL_GRAFANA_ADMIN_PASSWORD"
}

# kv/longhorn  (htpasswd_string is what the ExternalSecret reads)
VaultPut "longhorn" @{
    "htpasswd_string" = Require-Env "PLATFORMCTL_LONGHORN_HTPASSWD"
}

# kv/alertmanager
VaultPut "alertmanager" @{
    "discord_webhook_url" = Require-Env "PLATFORMCTL_ALERTMANAGER_DISCORD_WEBHOOK"
}

# kv/dotablaze-tech-discord-bot-token
VaultPut "dotablaze-tech-discord-bot-token" @{
    "token" = Require-Env "PLATFORMCTL_DISCORD_BOT_TOKEN"
}

# kv/usersrole  (two separate jwt keys for non/prd envs)
VaultPut "usersrole" @{
    "jwt_key_non" = Require-Env "PLATFORMCTL_USERSROLE_JWT_KEY_NON"
    "jwt_key_prd" = Require-Env "PLATFORMCTL_USERSROLE_JWT_KEY_PRD"
}

# kv/jdwlabs-ai-keys  — API keys AND htpasswd_string in one put (openclaw-basic-auth reads htpasswd_string from this same path)
VaultPut "jdwlabs-ai-keys" @{
    "anthropic_api_key"  = Require-Env "PLATFORMCTL_JDWLABS_ANTHROPIC_API_KEY"
    "openai_api_key"     = Require-Env "PLATFORMCTL_JDWLABS_OPENAI_API_KEY"
    "openrouter_api_key" = Require-Env "PLATFORMCTL_JDWLABS_OPENROUTER_API_KEY"
    "htpasswd_string"    = Require-Env "PLATFORMCTL_JDWLABS_OPENCLAW_HTPASSWD"
}

# kv/jdwlabs-github-app
VaultPut "jdwlabs-github-app" @{
    "github_app_id"              = Require-Env "PLATFORMCTL_JDWLABS_GITHUB_APP_ID"
    "github_app_installation_id" = Require-Env "PLATFORMCTL_JDWLABS_GITHUB_INSTALLATION_ID"
    "github_app_private_key"     = Require-Env "PLATFORMCTL_JDWLABS_GITHUB_PRIVATE_KEY"
}

# kv/dotablaze-tech-github-app
VaultPut "dotablaze-tech-github-app" @{
    "github_app_id"              = Require-Env "PLATFORMCTL_DOTABLAZE_GITHUB_APP_ID"
    "github_app_installation_id" = Require-Env "PLATFORMCTL_DOTABLAZE_GITHUB_INSTALLATION_ID"
    "github_app_private_key"     = Require-Env "PLATFORMCTL_DOTABLAZE_GITHUB_PRIVATE_KEY"
}

# kv/rclone-gdrive
VaultPut "rclone-gdrive" @{
    "rclone_conf" = Require-Env "PLATFORMCTL_RCLONE_CONF"
}

Write-Host "`nAll Vault KV paths seeded successfully." -ForegroundColor Green
Write-Host "ESO will refresh ExternalSecrets within 1 minute (or 1h for rclone-gdrive/alertmanager)." -ForegroundColor Cyan
