# Platform Bootstrap

Step-by-step guide for bringing the platform up on a fresh Kubernetes cluster. For architecture details,
see [ARCHITECTURE.md](ARCHITECTURE.md). For adding tenants after the platform is running,
see [ONBOARDING.md](ONBOARDING.md).

## Prerequisites

- [ ] Kubernetes cluster provisioned via `jdwlabs/infrastructure` (Terraform + `talops`)
- [ ] All nodes in Ready state: `kubectl get nodes`
- [ ] `kubeconfig` context set: `kubectl config current-context`
- [ ] `kubectl` and `jq` installed locally
- [ ] Platform repo cloned: `git clone https://github.com/jdwlabs/platform.git`

## Phase 1: Install ArgoCD

ArgoCD must exist before it can manage itself. Install it once manually to seed the cluster, then the platform takes
over via the `argo-cd` Helm chart in `tenants/platform/tenant.yaml` - managing ArgoCD's configuration, version, and
upgrades through GitOps from that point forward.

```bash
kubectl create namespace argocd
kubectl apply -n argocd --server-side --force-conflicts -f https://raw.githubusercontent.com/argoproj/argo-cd/stable/manifests/install.yaml
```

Wait for all pods to be ready:

```bash
kubectl wait --for=condition=Available deployment --all -n argocd --timeout=120s
```

Verify:

```bash
kubectl get pods -n argocd
```

All pods should be Running and Ready. Do not configure ArgoCD repositories or applications manually - the bootstrap
manifests handle everything. After Phase 2, the platform's `argo-cd` service (wave 0) will reconcile ArgoCD to the
Helm-managed version, replacing the manually-installed manifests.

### Retrieve ArgoCD admin credentials

ArgoCD generates a random admin password on first install, stored in the `argocd-initial-admin-secret` Secret:

```bash
ARGOCD_PASS=$(kubectl -n argocd get secret argocd-initial-admin-secret -o jsonpath='{.data.password}' | base64 -d)
echo "$ARGOCD_PASS"
```

Login via CLI (optional - not required for bootstrap, but useful for debugging):

```bash
argocd login argocd.jdwlabs.com --username admin --password "$ARGOCD_PASS"
```

Change the default password after the platform is stable:

```bash
argocd account update-password
```

After changing the password, delete the initial secret:

```bash
kubectl -n argocd delete secret argocd-initial-admin-secret
```

## Phase 2: Apply root-app

This single command starts the entire automated cascade:

```bash
kubectl apply -f https://raw.githubusercontent.com/jdwlabs/platform/main/bootstrap/root-app.yaml
```

The `bootstrap` Application uses the `default` AppProject (which always exists in ArgoCD) and recursively applies
everything in `bootstrap/`, which creates:

1. `bootstrap` AppProject (`bootstrap/argocd/projects/project-bootstrap.yaml`)
2. `governance` ApplicationSet (`bootstrap/governance-appset.yaml`)
3. Itself (self-managing via `selfHeal: true` and `prune: true`)

Verify:

```bash
kubectl get application bootstrap -n argocd
kubectl get appproject bootstrap -n argocd
kubectl get applicationset governance -n argocd
```

The `bootstrap` Application should show `Synced`. From this point forward, all changes go through Git - do not manually
edit ArgoCD objects owned by GitOps.

## Phase 3: Automated cascade

No user action required. This section explains what happens automatically.

The `governance` ApplicationSet scans `tenants/*/tenant.yaml` and creates one governance Application per tenant:
`governance-<tenant>`.

Each governance Application renders the `tenant-envelope` Helm chart, which creates per-tenant:

- Namespaces with labels and Pod Security Standards
- ResourceQuotas and LimitRanges
- NetworkPolicies
- ArgoCD AppProject scoped to tenant namespaces
- ARC RBAC for runner namespaces
- `<tenant>-services` ApplicationSet
- `<tenant>-deployments` ApplicationSet (if `deploymentRepo.url` set)

Platform services then deploy in sync wave order:

| Wave | What deploys                                                                     |
|------|----------------------------------------------------------------------------------|
| 0    | argo-cd (self-management)                                                        |
| 1    | cert-manager, porkbun-webhook, ingress-nginx, longhorn                           |
| 2    | vault, external-secrets, vault-config-operator, monitoring stack, atlas-operator |
| 3    | cnpg-operator, arc-systems                                                       |
| 4    | postgresql clusters, db-ui                                                       |

Watch sync progress:

```bash
kubectl get applications -n argocd --watch
```

Services at wave 2+ will remain degraded until Vault is initialized in Phase 4. This is expected - proceed immediately.

## Phase 4: Initialize Vault

Vault deploys at wave 2 but starts sealed and uninitialized. The External Secrets Operator, cert-manager DNS
credentials, and all tenant secrets depend on Vault. This phase cannot be automated.

### 4.1 Wait for Vault pod

```bash
kubectl wait --for=condition=Ready pod -l app.kubernetes.io/name=vault -n vault --timeout=120s
```

### 4.2 Initialize Vault

```bash
kubectl exec -n vault platform-vault-0 -- vault operator init \
  -key-shares=1 \
  -key-threshold=1 \
  -format=json > vault-init.json
```

Extract the values:

```bash
UNSEAL_KEY=$(jq -r '.unseal_keys_b64[0]' vault-init.json)
ROOT_TOKEN=$(jq -r '.root_token' vault-init.json)
```

**Store `vault-init.json` securely offline.** If the unseal key is lost, Vault data cannot be recovered after a pod
restart.

### 4.3 Unseal Vault

```bash
kubectl exec -n vault platform-vault-0 -- vault operator unseal "$UNSEAL_KEY"
```

### 4.4 Create Kubernetes secrets

These secrets are consumed by the `vault-auto-unseal` CronJob, the `vault-admin-initializer` Job, and the
`ClusterSecretStore`.

```bash
# Unseal key - vault-auto-unseal CronJob (runs every 2 min)
kubectl create secret generic vault-unseal-keys \
  -n vault \
  --from-literal=unseal_key_1="$UNSEAL_KEY"

# Root token - vault-admin-initializer Job
kubectl create secret generic vault-token \
  -n vault \
  --from-literal=token="$ROOT_TOKEN"

# Root token - ClusterSecretStore (for External Secrets Operator)
kubectl create secret generic vault-token \
  -n external-secrets \
  --from-literal=token="$ROOT_TOKEN"
```

### 4.5 Enable KV secrets engine

```bash
kubectl exec -n vault platform-vault-0 -- sh -c \
  "VAULT_TOKEN=$ROOT_TOKEN vault secrets enable -path=kv kv-v2"
```

### 4.6 Verify Vault automation

The `vault-admin-initializer` Job runs automatically once the `vault-token` secret exists. It enables Kubernetes auth,
the database secrets engine, and writes the `vault-admin` policy.

```bash
kubectl annotate application platform-vault -n argocd argocd.argoproj.io/refresh=normal --overwrite
kubectl get job vault-admin-initializer -n vault
kubectl logs job/vault-admin-initializer -n vault
```

The `vault-auto-unseal` CronJob runs every 2 minutes to re-unseal after pod restarts:

```bash
kubectl get cronjob vault-auto-unseal -n vault
```

Verify the ClusterSecretStore is ready:

```bash
kubectl get clustersecretstore vault
```

Expected: status shows `Valid`.

## Phase 5: Seed Vault secrets

ExternalSecrets across the platform pull from Vault KV v2 at the `kv/` mount. Until these paths exist, dependent
services will remain in error state.

### Platform secrets

```bash
# Porkbun DNS API credentials (cert-manager DNS01 webhook)
kubectl exec -n vault platform-vault-0 -- sh -c \
  "VAULT_TOKEN=$ROOT_TOKEN vault kv put kv/porkbun \
    api-key=<porkbun-api-key> \
    secret-key=<porkbun-secret-key>"

# Longhorn UI basic auth
kubectl exec -n vault platform-vault-0 -- sh -c \
  "VAULT_TOKEN=$ROOT_TOKEN vault kv put kv/longhorn \
    htpasswd_string=<htpasswd-value>"
```

### Tenant secrets

```bash
# jdwlabs ARC GitHub App
kubectl exec -n vault platform-vault-0 -- sh -c \
  "VAULT_TOKEN=$ROOT_TOKEN vault kv put kv/jdwlabs-github-app \
    github_app_id=<app-id> \
    github_app_installation_id=<installation-id> \
    github_app_private_key=<pem-private-key>"

# dotablaze-tech ARC GitHub App
kubectl exec -n vault platform-vault-0 -- sh -c \
  "VAULT_TOKEN=$ROOT_TOKEN vault kv put kv/dotablaze-tech-github-app \
    github_app_id=<app-id> \
    github_app_installation_id=<installation-id> \
    github_app_private_key=<pem-private-key>"
```

### Application secrets (jdwlabs deployments)

```bash
# usersrole JWT signing keys
kubectl exec -n vault platform-vault-0 -- sh -c \
  "VAULT_TOKEN=$ROOT_TOKEN vault kv put kv/usersrole \
    jwt_key_non=<jwt-secret-non> \
    jwt_key_prd=<jwt-secret-prd>"
```

### Application secrets (dotablaze-tech deployments)

```bash
# Discord bot token for meowbot
kubectl exec -n vault platform-vault-0 -- sh -c \
  "VAULT_TOKEN=$ROOT_TOKEN vault kv put kv/dotablaze-tech-discord-bot-token \
    token=<discord-bot-token>"
```

CNPG-generated secrets (`platform-postgresql-cluster-non-app`, `platform-postgresql-cluster-prd-app`) are created
automatically by the CNPG operator and read via the Kubernetes SecretStore - no manual seeding needed.

To discover all ExternalSecret Vault paths in the codebase:

```bash
grep -r 'remoteRef' tenants/ --include='*.yaml' -A 2 | grep 'key:'
```

## Phase 6: Verify convergence

After Vault secrets are in place, all dependent chains resolve automatically within 5-10 minutes.

### ExternalSecrets

```bash
kubectl get externalsecrets -A
```

All should show `SecretSynced` / `True`.

### cert-manager ClusterIssuers

```bash
kubectl get clusterissuer
```

`letsencrypt-prod` and `letsencrypt-staging` should show `Ready: true`.

### ArgoCD Applications

```bash
kubectl get applications -n argocd
```

All should show `Synced` and `Healthy`.

### ARC runners

```bash
kubectl get pods -n jdwlabs-runners
kubectl get pods -n dotablaze-tech-runners
```

Runners should appear in each GitHub org's Settings > Actions > runners within a few minutes.

### CNPG clusters

```bash
kubectl get cluster -n database
```

Both `platform-postgresql-cluster-non` and `platform-postgresql-cluster-prd` should show `Cluster in healthy state`.

Verify tenant databases were created automatically:

```bash
kubectl get databases -n database
```

Expected databases: `jdwlabs_non`, `dotablazetech_non` (non cluster), `jdwlabs_prd`, `dotablazetech_prd` (prd cluster).
These are declared in the CNPG cluster values and managed by the `Database` CRD - no manual creation needed.

### Tenant deployments

```bash
kubectl get applications -n argocd -l 'app.kubernetes.io/instance=jdwlabs-deployments'
```

All `jdwlabs-*` deployment Applications should show `Synced` and `Healthy`.

### Platform UIs

| Service               | URL                              |
|-----------------------|----------------------------------|
| ArgoCD                | `https://argocd.jdwlabs.com`     |
| Vault                 | `https://vault.jdwlabs.com`      |
| Database UI (Adminer) | `https://dbui.jdwlabs.com`       |
| Grafana               | `https://grafana.jdwlabs.com`    |
| Prometheus            | `https://prometheus.jdwlabs.com` |
| Longhorn              | `https://longhorn.jdwlabs.com`   |
| Kubernetes Dashboard  | `https://dashboard.jdwlabs.com`  |

### Accessing Grafana

The Grafana Helm chart generates a random admin password on first install, stored in the `platform-grafana` Secret:

```bash
GRAFANA_PASS=$(kubectl -n monitoring get secret platform-grafana -o jsonpath='{.data.admin-password}' | base64 -d)
echo "$GRAFANA_PASS"
```

Login at `https://grafana.jdwlabs.com` with username `admin` and the password above.

Two datasources are pre-configured:

| Datasource | Type       | URL                                      |
|------------|------------|------------------------------------------|
| Prometheus | Prometheus | `http://platform-prometheus-server:9000` |
| Loki       | Loki       | `http://platform-loki:3100`              |

To change the admin password, go to **Profile > Change password** in the Grafana UI, or:

```bash
kubectl -n monitoring exec deploy/platform-grafana -- \
  grafana cli admin reset-admin-password <new-password>
```

### Connecting to databases via db-ui

Adminer at `https://dbui.jdwlabs.com` provides browser-based access to the PostgreSQL clusters. No credentials are
pre-configured - you supply them on the login page.

**1. Get cluster credentials**

CNPG creates a superuser and an app-level secret per cluster. Retrieve them with:

```bash
# Non-production superuser
kubectl -n database get secret platform-postgresql-cluster-non-superuser \
  -o jsonpath='{.data.username}' | base64 -d; echo
kubectl -n database get secret platform-postgresql-cluster-non-superuser \
  -o jsonpath='{.data.password}' | base64 -d; echo
  
# Production superuser
kubectl -n database get secret platform-postgresql-cluster-prd-superuser \
  -o jsonpath='{.data.username}' | base64 -d; echo
kubectl -n database get secret platform-postgresql-cluster-prd-superuser \
  -o jsonpath='{.data.password}' | base64 -d; echo
 
# Non-production app-level credentials (used by application pods) 
kubectl -n database get secret platform-postgresql-cluster-non-app \
  -o jsonpath='{.data.username}' | base64 -d; echo
kubectl -n database get secret platform-postgresql-cluster-non-app \
  -o jsonpath='{.data.password}' | base64 -d; echo
  
# Production app-level credentials (used by application pods) 
kubectl -n database get secret platform-postgresql-cluster-prd-app \
  -o jsonpath='{.data.username}' | base64 -d; echo
kubectl -n database get secret platform-postgresql-cluster-prd-app \
  -o jsonpath='{.data.password}' | base64 -d; echo
```

**2. Login to Adminer**

Open `https://dbui.jdwlabs.com` and fill in:

| Field    | Non-production                                    | Production                                        |
|----------|---------------------------------------------------|---------------------------------------------------|
| System   | PostgreSQL                                        | PostgreSQL                                        |
| Server   | `platform-postgresql-cluster-non-rw.database.svc` | `platform-postgresql-cluster-prd-rw.database.svc` |
| Username | *(from superuser or app secret above)*            | *(from superuser or app secret above)*            |
| Password | *(from secret above)*                             | *(from secret above)*                             |
| Database | `jdwlabs_non` / `dotablazetech_non`               | `jdwlabs_prd` / `dotablazetech_prd`               |

Read-only endpoints are also available at `platform-postgresql-cluster-{non,prd}-ro.database.svc`.

**3. Available databases**

| Cluster                           | Database            | Schemas  | Owner          | Owner Role                |
|-----------------------------------|---------------------|----------|----------------|---------------------------|
| `platform-postgresql-cluster-non` | `jdwlabs_non`       | `auth`   | jdwlabs        | `jdwlabs_non_owner`       |
| `platform-postgresql-cluster-non` | `dotablazetech_non` | `public` | dotablaze-tech | `dotablazetech_non_owner` |
| `platform-postgresql-cluster-prd` | `jdwlabs_prd`       | `auth`   | jdwlabs        | `jdwlabs_prd_owner`       |
| `platform-postgresql-cluster-prd` | `dotablazetech_prd` | `public` | dotablaze-tech | `dotablazetech_prd_owner` |

Databases and owner roles are declared in the CNPG cluster values (`postgresql-cluster-{non,prd}/values.yaml`) and
created automatically via the CNPG `Database` CRD at wave 4. Schema migrations are managed by the Atlas operator
(wave 5). Do not manually alter tables or databases - changes should go through CNPG values for databases/roles and
AtlasSchema ConfigMaps in `tenants/*/services/*-schemas/` for DDL.

### Re-issuing TLS certificates

If the Porkbun API keys were not available when cert-manager first attempted DNS-01 validation (e.g., Vault was not yet
seeded), all ingress certificates will be in a failed state. After seeding `kv/porkbun` in Phase 5, force cert-manager
to retry by deleting the TLS secrets - cert-manager will automatically re-create them:

```bash
kubectl delete secret argo-cd-tls -n argocd
kubectl delete secret db-ui-tls -n database
kubectl delete secret grafana-tls -n monitoring
kubectl delete secret kubernetes-dashboard-tls -n kubernetes-dashboard
kubectl delete secret longhorn-tls -n longhorn-system
kubectl delete secret prometheus-tls -n monitoring
kubectl delete secret vault-tls -n vault
```

Verify certificates are re-issued:

```bash
kubectl get certificates -A
```

All should show `Ready: True` within a few minutes. If any remain `False`, check the certificate request:

```bash
kubectl get certificaterequest -A
kubectl describe certificaterequest <name> -n <namespace>
```

Common issues:

- `porkbun` secret not synced - check `kubectl get externalsecret porkbun -n cert-manager`
- ClusterIssuer not ready - check `kubectl get clusterissuer letsencrypt-prod`
- DNS propagation delay - wait 2-5 minutes and check again

## Dependency Chain

```
Kubernetes cluster ready
  |
  +-- ArgoCD installed (Phase 1)
       |
       +-- bootstrap/root-app.yaml applied (Phase 2)
            |
            +-- Governance cascade (Phase 3, automated)
                 |
                 +-- Wave 0: ArgoCD (self-managed)
                 |
                 +-- Wave 1: cert-manager, ingress-nginx, Longhorn
                 |
                 +-- Wave 2: Vault deployed (sealed)
                 |    |
                 |    +-- Vault initialized + secrets created (Phase 4)  <-- MANUAL
                 |         |
                 |         +-- ClusterSecretStore becomes Valid
                 |              |
                 |              +-- Vault KV paths seeded (Phase 5)  <-- MANUAL
                 |                   |
                 |                   +-- ExternalSecrets resolve
                 |                   |    +-- cert-manager ClusterIssuers --> TLS certs
                 |                   |    +-- Longhorn ingress auth
                 |                   |    +-- ARC runners register with GitHub
                 |                   |
                 |                   +-- Tenant deployment apps resolve
                 |                        +-- usersrole JWT secret
                 |
                 +-- Wave 3: CNPG operator, ARC controller
                 |
                 +-- Wave 4: PostgreSQL clusters, db-ui
                 |
                 +-- Wave 5: Tenant ARC runner sets, Atlas schema migrations
```

## Troubleshooting

| Symptom                                | Likely cause                                | Resolution                                    |
|----------------------------------------|---------------------------------------------|-----------------------------------------------|
| `governance-*` apps stuck Progressing  | Namespace creation in progress              | Wait 2-3 min, check `kubectl get ns`          |
| ExternalSecrets in `SecretSyncedError` | Vault not initialized or secrets not seeded | Complete Phase 4 and 5                        |
| `letsencrypt-prod` issuer not Ready    | Porkbun secret missing from Vault           | Seed `kv/porkbun` (Phase 5)                   |
| Vault pod CrashLoopBackOff             | Initialized but unseal key secret missing   | Create `vault-unseal-keys` secret (Phase 4.4) |
| ARC runner pods not appearing          | GitHub App secret not seeded                | Seed `kv/<tenant>-github-app` (Phase 5)       |
| `vault-admin-initializer` Job failed   | `vault-token` secret missing in vault ns    | Create `vault-token` secret (Phase 4.4)       |
| Deployment apps stuck `Missing`        | Deployment repo not accessible              | Check ArgoCD repo credentials                 |
| CNPG clusters not healthy              | Longhorn storage not ready                  | Check Longhorn pods in `longhorn-system`      |
