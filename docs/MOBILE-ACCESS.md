# Mobile Access (Headlamp)

How to reach `https://dashboard.jdwlabs.com` (Headlamp) from a phone that is
**not** on the home network, what to expect from the login flow, and which
auth options were evaluated. See [OPERATIONS.md §1.2](OPERATIONS.md#12-headlamp-mobile-login-oidc-via-dex)
for credential rotation and the apiserver trust chain.

## 1. How it works

No VPN, Tailscale, or kubeconfig is required. The dashboard is plain public
HTTPS end to end:

```
phone browser ──HTTPS──▶ dashboard.jdwlabs.com (platform-gateway, Let's Encrypt)
       │                        │
       │   redirect             ▼
       └──────────▶ argocd.jdwlabs.com/api/dex  (Dex, bundled with ArgoCD)
                                │
                  id_token      ▼
            Headlamp ──bearer──▶ kube-apiserver (trusts the Dex issuer)
```

Headlamp forwards your Dex `id_token` as the bearer token on every Kubernetes
API call; the `headlamp-oidc-admin` ClusterRoleBinding maps
`oidc:admin@jdwlabs.com` to `cluster-admin`.

## 2. One-time phone setup

1. Save the Dex credentials (email `admin@jdwlabs.com`, password from
   `kv/argocd-dex`) in your phone's password manager — 1Password autofill
   works on both the iOS and Android Dex login form.
2. Open `https://dashboard.jdwlabs.com` and add it to your home screen
   (**Share → Add to Home Screen** on iOS, **⋮ → Add to Home screen** on
   Android). This gives a one-tap, full-screen entry point.

## 3. Day-to-day login flow

**Steady state — 1 tap.** Tap the home-screen icon. Headlamp holds a refresh
token (the OIDC client requests `offline_access`), so the 24-hour Dex
`id_token` is renewed silently in the background and you land directly on the
cluster view without seeing Dex at all.

**Cold login** (first ever login, or after a `dex-server`/`headlamp` pod
restart wipes the refresh-token state):

1. Tap the home-screen icon.
2. Tap **Sign In** — you are redirected to the Dex form at
   `https://argocd.jdwlabs.com/api/dex/auth`.
3. Tap the password-manager autofill suggestion, then **Login**.

Dex's approval screen is skipped (`oauth2.skipApprovalScreen: true`), so
there is no extra consent tap and you bounce straight back to Headlamp.

Tap budget: steady state is 1 tap (home-screen icon only), which meets the
≤3-tap goal for day-to-day use. A cold login is 4 taps with autofill
(icon → Sign In → autofill → Login); the Google connector in §6 is the only
evaluated option that brings the cold path under 3 taps. Manual password
typing only happens if autofill is not set up.

## 4. Session lifetime

| Thing                  | Lifetime                                                                |
|------------------------|-------------------------------------------------------------------------|
| Dex `id_token`         | 24 h (Dex default) — renewed silently via the refresh token             |
| Dex refresh token      | No expiry (Dex default), **but** held in memory: a `dex-server` pod restart invalidates it |
| Headlamp refresh cache | In-memory: a `headlamp` pod restart also forces a cold login            |

In practice: expect a cold login only after pod restarts (upgrades, node
reboots), not on a daily timer.

## 5. Friction points and options evaluated

| Option                          | Verdict          | Reasoning                                                                                       |
|---------------------------------|------------------|--------------------------------------------------------------------------------------------------|
| OIDC via Dex (static password)  | **In place**     | Single login form, password-manager friendly, no kubeconfig/token paste on the phone             |
| `offline_access` refresh tokens | **Implemented**  | Pure repo config (Headlamp OIDC scopes); removes the daily re-login, the main mobile pain point   |
| Passkey / WebAuthn              | Ruled out        | Dex has no native WebAuthn/passkey connector; would require replacing Dex (e.g. Authelia, Keycloak) — not worth it for a single-operator homelab |
| Google upstream connector       | Viable, deferred | One-tap on phones already signed into Google, but needs a Google Cloud OAuth client created out-of-repo first — see §6 |
| VPN / Tailscale / WireGuard     | Ruled out        | Unnecessary: the dashboard is already public HTTPS behind the platform gateway with valid certs   |

Remaining friction after this change: password entry on a cold login
(mitigated by autofill), and re-login after Dex/Headlamp pod restarts
(unavoidable with in-memory token storage in ArgoCD's bundled Dex).

## 6. Optional upgrade: Google one-tap login (not implemented)

Adding Google as an upstream Dex connector turns the cold login into a single
"Continue as <you>" tap on any phone already signed into a Google account —
no password at all. It is not implemented because it requires creating an
OAuth client in the Google Cloud console (a manual, out-of-repo step). If you
want it:

1. Create an OAuth 2.0 Client ID (type *Web application*) in the Google Cloud
   console with authorized redirect URI
   `https://argocd.jdwlabs.com/api/dex/callback`.
2. Add `google-client-id` and `google-client-secret` fields to `kv/argocd-dex`
   (extend the seed spec in `cli/internal/bootstrap/phase4_vault_seed.go` and
   the `dex-secrets` ExternalSecret in
   `tenants/platform/services/argo-cd/postInstall/dex-externalsecret.yaml`).
   Secret **names** only in git — values live in Vault, as with the existing
   `headlamp-client-secret`.
3. Add the connector to `dex.config` in
   `tenants/platform/services/argo-cd/values.yaml`:

   ```yaml
   connectors:
     - type: google
       id: google
       name: Google
       config:
         clientID: $dex-secrets:google-client-id
         clientSecret: $dex-secrets:google-client-secret
         redirectURI: https://argocd.jdwlabs.com/api/dex/callback
   ```

4. The Google account's email must be `admin@jdwlabs.com` to match the
   existing `headlamp-oidc-admin` ClusterRoleBinding; for any other address,
   add a binding for `oidc:<that-email>`.

> With both the static-password DB and a Google connector enabled, Dex shows
> a connector picker (one extra tap). Drop `enablePasswordDB: true` to make
> Google the only — and therefore automatic — connector, at the cost of
> losing the password fallback.

## 7. Troubleshooting

| Symptom                                       | Fix                                                                                      |
|-----------------------------------------------|-------------------------------------------------------------------------------------------|
| `401` after login (Headlamp shows auth error) | kube-apiserver OIDC flags missing/wrong (Talos config, infrastructure repo) or the `headlamp-oidc-admin` ClusterRoleBinding is absent |
| Redirect loop between Headlamp and Dex        | `headlamp-oidc-secret` out of date — check the ExternalSecret in the `headlamp` namespace, then restart the Headlamp pod |
| Dex rejects the password                      | Hash in Vault out of sync — rotate per [OPERATIONS.md §1.2](OPERATIONS.md#12-headlamp-mobile-login-oidc-via-dex) |
| Daily forced re-login returns                 | Refresh token not being issued — confirm `OIDC_SCOPES` includes `offline_access` in `tenants/platform/services/headlamp/postInstall/oidc-externalsecret.yaml` |
