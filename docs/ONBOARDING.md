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

### Step 2: Create Tenant Directory

```bash
mkdir -p tenants/<tenant>/service/arc-runner-set-<tenant>/postInstall
```

### Step 3: Create tenant.yaml

Create `tenants/<tenant>/tenant.yaml` using an existing tenant as a reference (e.g. `tenants/jdwlabs/tenant.yaml`). Update:

- [ ] `name`, `displayName`, `githubOrg`, `contacts`
- [ ] `deploymentRepo` (if the tenant has a deployment repo)
- [ ] `namespaces` list with appropriate `quotaTier` and `networkPolicy` values
- [ ] `project.elevated` (should be `false` for non-platform tenants)
- [ ] `services` list with ARC runner set and any schema services

The `tenant-envelope` Helm chart automatically provisions:
- Namespaces with labels and Pod Security Standards
- ResourceQuotas and LimitRanges (based on `quotaTier`)
- NetworkPolicies (based on `networkPolicy`)
- ArgoCD AppProject scoped to tenant namespaces
- ARC RBAC for runner namespaces
- ApplicationSets for services and deployments

### Step 4: Create Service Configs

- [ ] Create `tenants/<tenant>/services/arc-runner-set-<tenant>/values.yaml`
- [ ] Create `tenants/<tenant>/services/arc-runner-set-<tenant>/postInstall/externalsecret.yaml`

### Step 5: Git and ArgoCD

- [ ] Open pull request; CI validation must pass
- [ ] Merge to main
- [ ] The governance ApplicationSet detects the new `tenant.yaml` and generates all resources within 3 minutes

### Step 6: Git and ArgoCD

- [ ] ArgoCD Application `governance-<tenant>` shows Synced/Healthy
- [ ] ArgoCD Application `<tenant>-arc-runner-set-<tenant>` shows Synced/Healthy
- [ ] Pods running in `<tenant>-runners` namespace
- [ ] Runner appears in GitHub org Settings > Actions > Runners
- [ ] NetworkPolicies applied: `kubectl get netpol -n <tenant>-runners`
- [ ] ResourceQuota applied: `kubectl describe resourcequota -n <tenant>-runners`

## Offboarding a Tenant

1. Remove `tenants/<tenant>/` directory from git
2. The governance ApplicationSet stops finding the `tenant.yaml`
3. ArgoCD prunes all Application objects for that tenant
4. Manually delete namespaces if needed: `kubectl delete namespace <tenant>-runners`
5. Delete Vault secrets under `kv/<tenant>/`
