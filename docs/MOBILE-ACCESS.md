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

1. Tap the home-screen icon, then **Sign In**.
2. Dex shows a connector picker (both login methods are enabled). Tap
   **Log in with GitHub**.
3. GitHub drops you straight back to the cluster view if your phone already
   holds a GitHub session; otherwise approve with a passkey/biometric tap.

The password form remains on the same Dex picker as a fallback (email
`admin@jdwlabs.com` + autofill). Dex's approval screen is skipped
(`oauth2.skipApprovalScreen: true`), so there is no extra consent tap.

Tap budget: steady state is 1 tap (home-screen icon only). A cold login via
GitHub is 2–3 taps (icon/Sign In → Log in with GitHub → optional passkey),
meeting the ≤3-tap goal even on the cold path when the phone is already
signed into GitHub. The password fallback is 4 taps with autofill.

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
| GitHub upstream connector       | **Implemented**  | Org-gated (`orgs: [jdwlabs]`) social login; phones already signed into GitHub get a 2–3 tap cold login with passkey/biometric — see §6 |
| Passkey / WebAuthn (direct)     | Ruled out        | Dex has no native WebAuthn connector; the GitHub connector delivers passkey login via GitHub's own auth instead of replacing Dex |
| Google upstream connector       | **Implemented**  | Equivalent UX for Google-centric phones. A personal-gmail account has no `hostedDomains` filter, so any Google account can *authenticate* — authorization is gated entirely by the per-email RBAC grants (see §6.1) |
| VPN / Tailscale / WireGuard     | Ruled out        | Unnecessary: the dashboard is already public HTTPS behind the platform gateway with valid certs   |

Remaining friction after this change: re-login after Dex/Headlamp pod
restarts (unavoidable with in-memory token storage in ArgoCD's bundled Dex),
and the extra connector-picker tap that appears because the static-password
fallback is kept enabled alongside GitHub.

## 6. GitHub login setup (implemented — requires one-time operator steps)

The GitHub upstream connector is configured in the repo
(`tenants/platform/services/argo-cd/values.yaml`), but it cannot work until
the GitHub OAuth App exists and its credentials are in Vault. One-time steps:

1. Create a **GitHub OAuth App** (Settings → Developer settings → OAuth Apps,
   under the `jdwlabs` org or your account) with **Authorization callback URL**
   `https://argocd.jdwlabs.com/api/dex/callback`.
2. Put the client ID and secret in Vault at `kv/argocd-dex` as fields
   `github-client-id` and `github-client-secret` (extend the seed spec in
   `cli/internal/bootstrap/phase4_vault_seed.go`). The `dex-secrets`
   ExternalSecret already maps both fields — names in git, values in Vault.
3. Confirm the `oidc:<email>` subject in
   `tenants/platform/services/headlamp/postInstall/oidc-admin-rbac.yaml`
   matches your GitHub account's primary **verified** email. A mismatch fails
   closed (you authenticate but get no cluster-admin).

Access is gated to `jdwlabs` org members (`orgs: [jdwlabs]`); membership alone
does not grant cluster-admin — only emails bound in `headlamp-oidc-admin` do.

> With the static-password DB (`enablePasswordDB: true`) and the GitHub and
> Google connectors all enabled, Dex shows a connector picker (one extra
> tap). Drop `enablePasswordDB: true` and the `staticPasswords` block to
> shrink the picker to the two social connectors, at the cost of losing the
> password fallback.

## 6.1 Google login setup (implemented — requires one-time operator steps)

The Google upstream connector (`type: google`) shares the wiring above but
its access model is inverted: there is no equivalent of GitHub's
`orgs: [jdwlabs]` gate. `hostedDomains` only works for Google Workspace
domains, and the operator account is a personal Gmail — so **any Google
account in the world can complete the Dex login**. That is safe only because
authorization fails closed on both consumers:

- **Headlamp / Kubernetes**: RBAC is granted per email by the
  `headlamp-oidc-admin` ClusterRoleBinding
  (`oidc:jdwillmsen@gmail.com`); any other Google identity authenticates
  but holds a token with zero RBAC.
- **ArgoCD**: `policy.csv` grants `role:admin` to `jdwillmsen@gmail.com`
  only, and no `policy.default` is set, so unknown identities get no role.

Never add a `policy.default` or a wildcard RBAC subject while the Google
connector is enabled.

One-time steps (mirror of §6):

1. Create an **OAuth 2.0 Client ID** (type *Web application*) in the Google
   Cloud console (APIs & Services → Credentials) with authorized redirect
   URI `https://argocd.jdwlabs.com/api/dex/callback`.
2. Put the client ID and secret in Vault at `kv/argocd-dex` as fields
   `google-client-id` and `google-client-secret`
   (`platformctl bootstrap seed argocd-dex` with the
   `PLATFORMCTL_ARGOCD_DEX_GOOGLE_*` env vars). The `dex-secrets`
   ExternalSecret maps both fields — names in git, values in Vault.
3. Confirm the Google account's email matches the `oidc:<email>` subject in
   `tenants/platform/services/headlamp/postInstall/oidc-admin-rbac.yaml`
   and the `g, <email>, role:admin` grant in the ArgoCD `policy.csv`.

## 7. Troubleshooting

| Symptom                                       | Fix                                                                                      |
|-----------------------------------------------|-------------------------------------------------------------------------------------------|
| `401` after login (Headlamp shows auth error) | kube-apiserver OIDC flags missing/wrong (Talos config, infrastructure repo) or the `headlamp-oidc-admin` ClusterRoleBinding is absent |
| Redirect loop between Headlamp and Dex        | `headlamp-oidc-secret` out of date — check the ExternalSecret in the `headlamp` namespace, then restart the Headlamp pod |
| GitHub login shows "access denied"            | Account is not a `jdwlabs` org member, or the OAuth App callback URL ≠ `https://argocd.jdwlabs.com/api/dex/callback`, or `github-client-id`/`-secret` missing from `kv/argocd-dex` |
| GitHub login succeeds but `403`/no access     | GitHub primary email ≠ the `oidc:<email>` subject in `headlamp-oidc-admin` — update the ClusterRoleBinding |
| Google login fails at Google (`redirect_uri_mismatch` / invalid client) | OAuth client redirect URI ≠ `https://argocd.jdwlabs.com/api/dex/callback`, or `google-client-id`/`-secret` missing from `kv/argocd-dex` |
| Google login succeeds but `403`/no access     | Expected for every Google account except the granted email — the connector authenticates anyone; only `oidc:jdwillmsen@gmail.com` (Headlamp CRB) and the `policy.csv` grant (ArgoCD) authorize |
| Dex rejects the password                      | Hash in Vault out of sync — rotate per [OPERATIONS.md §1.2](OPERATIONS.md#12-headlamp-mobile-login-oidc-via-dex) |
| Daily forced re-login returns                 | Refresh token not being issued — confirm `OIDC_SCOPES` includes `offline_access` in `tenants/platform/services/headlamp/postInstall/oidc-externalsecret.yaml` |
