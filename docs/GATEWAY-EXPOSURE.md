# Gateway Exposure Inventory

Every hostname served by the shared `nginx-gateway/platform-gateway`, what
protects it, and whether it is meant to be reachable from the internet.
Companion to [OPERATIONS.md §1](OPERATIONS.md#1-day-2-access) (how to log in)
and [MOBILE-ACCESS.md](MOBILE-ACCESS.md) (Headlamp from off-LAN).

## 1. Exposure path

```
internet ──▶ router port-forward ──▶ HAProxy 192.168.1.199 ──▶ gateway NodePort 30180/30543 ──▶ HTTPRoute
```

- Public DNS resolves every `*.jdwlabs.com` name to the same address (wildcard
  CNAME to the apex, kept current by porkbun-ddns). A hostname is therefore
  internet-reachable the moment an HTTPRoute with that name attaches to the
  gateway.
- The gateway has two listeners, `http:80` and `https:443`, both hostname `*`.
  The `http` listener only carries the `http-to-https-redirect` route.
- The HAProxy `http-ingress` and `https-ingress` frontends accept any source
  address (`infrastructure/bootstrap/internal/haproxy/config.go`). Only the
  Kubernetes API and Talos API frontends use `AllowedAdminCIDRs`. **No source
  restriction exists between the internet and any route below.**
- A route's own `filters` (an `AuthenticationFilter` extension ref) or the
  backing app's login is the only protection.

## 2. How this was checked

Hostnames come from `kubectl get httproute -A -o json` cross-checked against
the HTTPRoute manifests on `main`. Evidence is one unauthenticated
`curl -s -o /dev/null -w '%{http_code}' -m8 https://<host>/`, taken
2026-10-17, no credentials sent. The probe ran from the devbox, so it proves
the gateway's behaviour, not that the router forwards the name; reachability
rests on the path in §1. Status is **OK** (exposure is intended and the auth
is adequate) or **Follow-up** (see §5).

## 3. Admin and observability hosts

| Host | Backend | Auth | GET `/` | Status |
|------|---------|------|---------|--------|
| `argocd.jdwlabs.com` | `argocd/argocd-server:80` | App login (Dex OIDC plus local); anonymous disabled | 200 (SPA shell) | Follow-up: admin UI, move to Tailscale-only |
| `grafana.jdwlabs.com` | `monitoring/platform-grafana:80` | App login (no anonymous auth) | 302 (to `/login`) | Follow-up: move to Tailscale-only |
| `vault.jdwlabs.com` | `vault/platform-vault-ui:8200` | App login (token); `/v1/sys/health` is unauthenticated by design and discloses version and seal state | 307 (to `/ui/`) | Follow-up: move to Tailscale-only |
| `dashboard.jdwlabs.com` | `headlamp/platform-headlamp:80` | App login (OIDC via Dex) | 200 (SPA shell) | Follow-up: ServiceAccount is bound to `cluster-admin`; internet-facing |
| `dbui.jdwlabs.com` | `database/db-ui:80` | Adminer's own DB login only; no gateway filter | 200 (login form) | Follow-up: raw database login on the public listener |
| `alertmanager.jdwlabs.com` | `monitoring/platform-kube-prometheus-s-alertmanager:9093` | Basic auth (`AuthenticationFilter alertmanager-auth`, `Opaque` credentials secret) | 401 | OK: must stay reachable for GitHub runners |
| `longhorn.jdwlabs.com` | `longhorn-system/longhorn-frontend:80` | Basic auth (`AuthenticationFilter longhorn-auth`, `Opaque` credentials secret) | 401 | Follow-up: single shared basic-auth credential in front of a storage admin UI; Tailscale-only preferred |

Removed from the gateway: `prometheus.jdwlabs.com` and `tempo.jdwlabs.com`
(no auth of their own; Grafana reaches both server-side). Both now return 404.
`db.jdwlabs.com` and `charts.jdwlabs.com` have no route and return 404.

The "prd Drift" workflow in the deployments repo posts to
`alertmanager.jdwlabs.com` with `ALERTMANAGER_USER` / `ALERTMANAGER_PASSWORD`
from repository secrets (basic auth), falling back to `ALERTMANAGER_TOKEN`
as a bearer token (`tools/notify-alert.py`). The gate holds: an
unauthenticated GET gets 401.

## 4. Application and webhook hosts

| Host | Backend | Auth | GET `/` | Status |
|------|---------|------|---------|--------|
| `authui.{non,prd}.jdwlabs.com` | `authui:8080` | None at gateway (SPA; login in-app) | 200 | OK: intended public |
| `usersui.{non,prd}.jdwlabs.com` | `usersui:8080` | None at gateway (SPA) | 200 | OK: intended public |
| `rolesui.{non,prd}.jdwlabs.com` | `rolesui:8080` | None at gateway (SPA) | 200 | OK: intended public |
| `container.{non,prd}.jdwlabs.com` | `container:8080` | None at gateway (SPA shell) | 200 | OK: intended public |
| `servicediscovery.{non,prd}.jdwlabs.com` | `servicediscovery:9000` | None; read-only manifest endpoints | 200 | OK: intended public |
| `usersrole.{non,prd}.jdwlabs.com` | `usersrole:8080` (non also `/api/users`, `/api/roles` to identity-service and `/api/profiles` to profile-service) | App auth (token required) | 401 | OK |
| `identity-service.non.jdwlabs.com` | `identity-service:8080`, `GET /actuator/info` only | None | 404 at `/`; `/actuator/info` 200 | Follow-up: build info on a public host; confirm intended |
| `profile-service.non.jdwlabs.com` | `profile-service:8080`, `GET /actuator/info` only | None | 404 at `/`; `/actuator/info` 200 | Follow-up: same as above |
| `pr-agent.jdwlabs.com` | `pr-review/pr-agent:3000`, `POST /api/v1/github_webhooks` only | GitHub webhook HMAC; fails closed when the secret is unset | 404 | OK: other paths and methods are gateway 404 |
| `meowbot.{non,prd}.jdwlabs.com` | `meowbot:8080` | None at gateway | 404 | OK: nothing served at `/`; confirm any endpoint is meant to be public |
| `fwb.prd.jdwlabs.com` | `jdwillmsen-prd/...-fwb-prd-map:8080` | None | 200 (HTML) | Follow-up: route not defined in this repo; confirm a public map is intended |

## 5. Open decisions

- **HAProxy source policy.** Keep `https-ingress` open to all sources, add an
  SNI allowlist (the intended-public names plus `alertmanager` for GitHub
  runners), or split admin hosts onto a LAN/CIDR-restricted frontend.
  Undecided.
- **Tailscale-only admin UIs.** ArgoCD, Grafana and Vault are planned to move
  off the public listener; Headlamp, Adminer and Longhorn are candidates.
  Off-LAN access depends on the Tailscale subnet router
  ([OPERATIONS.md §1.3](OPERATIONS.md#13-proxmox-cluster-ui-hypervisor-management)).
- **Headlamp privileges.** The Headlamp ServiceAccount holds `cluster-admin`
  (`tenants/platform/services/headlamp/values.yaml`); reduce it unless
  something needs it.

## 6. Keeping this current

Add a row whenever an HTTPRoute with a new hostname merges, and re-run the
probe in §2 after any change to the gateway, HAProxy or an `AuthenticationFilter`.
List routes with:

```bash
kubectl get httproute -A -o json | jq -r '.items[] | [.metadata.namespace + "/" + .metadata.name, (.spec.hostnames // [] | join(","))] | @tsv'
```
