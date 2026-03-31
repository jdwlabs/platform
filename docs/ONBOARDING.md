# Tenant Onboarding

## Adding a New Tenant

### Prerequisites

- [ ] Tenant's GitHub org URL confirmed
- [ ] GitHub App created in tenant's org for ARC runners
- [ ] Vault access to add secrets

### Step 1: Vault Secrets

- [ ] Create Vault path `kv/<tenant>/arc/github-app` with keys:
  - `github_app_id`
  - `github_app_installation_id`
  - `github_app_private_key`

### Step 2: Create Directory Structure

```bash
mkdir -p tenants/<tenant>/apps/arc-runner-set-<tenant>/postInstall
```

- [ ] Copy `tenants/jdwlabs/tenant.yaml` to `tenants/<tenant>/tenant.yaml`, update fields
- [ ] Create `tenants/<tenant>/config.yaml` with apps list

### Step 3: Create Namespace Baseline Manifests

For each namespace (`<tenant>-runners`):

- [ ] Copy `tenants/_templates/namespace.yaml` and update name + labels
- [ ] Copy `tenants/_templates/resource-quota.yaml` and update namespace
- [ ] Copy `tenants/_templates/limit-range.yaml` and update namespace
- [ ] Copy `tenants/_templates/network-policy-default-deny.yaml` and update namespace
- [ ] Copy `tenants/_templates/network-policy-allow-dns.yaml` and update namespace
- [ ] Copy `tenants/_templates/network-policy-allow-ingress.yaml` and update namespace

### Step 4: Create ArgoCD AppProject

- [ ] Copy `tenants/_templates/argocd-project.yaml`
- [ ] Update `metadata.name` to `tenant-<tenant>`
- [ ] Update `spec.destinations` to tenant's namespaces
- [ ] Save as `bootstrap/argocd/projects/proejct-tenant-<tenant>.yaml`
- [ ] Apply: `kubectl apply -f bootstrap/argocd/projects/project-tenant-<tenant>.yaml`

### Step 5: Create App Configs

- [ ] Create `tenants/<tenant>/apps/arc-runner-set-<tenant>/values.yaml`
- [ ] Create `tenants/<tenant>/apps/arc-runner-set-<tenant>/postInstall/externalsecret.yaml`

### Step 6: Git and ArgoCD

- [ ] `git add tenants/<tenant>/`
- [ ] `git add bootstrap/argocd/projects/project-tenant-<tenant>.yaml`
- [ ] Open pull request; CI validation must pass
- [ ] Merge to main
- [ ] Monitor ArgoCD: the tenant ApplicationSet should generate new Applications within 3 minutes

### Step 7: Verify

- [ ] ArgoCD Application `<tenant>-arc-runner-set-<tenant>` shows Synced/Healthy
- [ ] Pods running in `<tenant>-runners` namespace
- [ ] Runner appears in GitHub org Settings > Actions > Runners
- [ ] NetworkPolicies applied: `kubectl get netpol -n <tenant>-runners`
- [ ] ResourceQuota applied: `kubectl describe resourcequota -n <tenant>-runners`

## Offboarding a Tenant

1. Remove `tenants/<tenant>/` directory from git
2. The tenant ApplicationSet generator stops finding the config.yaml
3. ArgoCD prunes all Application objects for that tenant
4. Manually delete namespaces: `kubectl delete namespace <tenant>-runners`
5. Delete Vault secrets under `kv/<tenant>/`
6. Delete ArgoCD AppProject: `kubectl delete appproject tenant-<tenant> -n argocd`
7. Remove `bootstrap/argocd/projects/project-tenant-<tenant>.yaml` from git
