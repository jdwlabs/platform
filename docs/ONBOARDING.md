# Tenant Onboarding

## Adding a New Tenant

### Prerequisites

- [ ] Tenant's GitHub org URL confirmed
- [ ] GitHub App created in tenant's org for ARC runners (only if enabling self-hosted runners — ARC is dormant by default, CI runs on GitHub-hosted runners; see OPERATIONS.md)
- [ ] `platformctl` installed (see [BOOTSTRAP.md §2](BOOTSTRAP.md#2-install-platformctl))

### Step 1: Clone the platform repo and add your tenant

```bash
git clone https://github.com/jdwlabs/platform.git
cd platform
mkdir -p tenants/<tenant>
```

### Step 2: Create `tenant.yaml`

Create `tenants/<tenant>/tenant.yaml` using an existing tenant as a
reference (e.g. `tenants/jdwlabs/tenant.yaml`). Update:

- [ ] `name`, `displayName`, `githubOrg`, `contacts`
- [ ] `deploymentRepo` (if the tenant has a deployment repo — see
      [TENANT-MODEL.md](TENANT-MODEL.md#deploymentrepourl))
- [ ] `namespaces` list with appropriate `quotaTier` and `networkPolicy`
- [ ] `project.elevated` (should be `false` for non-platform tenants)
- [ ] `services` list with ARC runner set and any schema services

The `tenant-envelope` Helm chart automatically provisions namespaces,
ResourceQuotas, LimitRanges, NetworkPolicies, AppProject, ARC RBAC, and
ApplicationSets — no manual resource creation.

### Step 3: Validate your `tenant.yaml`

```bash
platformctl tenants validate tenants/
```

A non-zero exit means the file is missing required fields or contains a
malformed service entry. The error message tells you exactly which field.

### Step 4: Create service configs

- [ ] (Only if enabling self-hosted ARC runners) Create `tenants/<tenant>/services/arc-runner-set-<tenant>/values.yaml`
- [ ] (Only if enabling self-hosted ARC runners) Create `tenants/<tenant>/services/arc-runner-set-<tenant>/postInstall/externalsecret.yaml`

### Step 5: Set up a deployment repo (optional)

If the tenant manages application deployments in a separate Git repository:

1. Create the repo (e.g. `github.com/<org>/deployments`)
2. Set `deploymentRepo.url` and `deploymentRepo.revision` in `tenant.yaml`
3. Create per-environment config files following the structure in
   [ARCHITECTURE.md](ARCHITECTURE.md#services-deployment)

### Step 6: Seed Vault secrets

Vault secrets are seeded by `platformctl bootstrap phase 4`. See
[TENANT-MODEL.md §Tenant secret seeding](TENANT-MODEL.md#tenant-secret-seeding)
for the per-tenant paths and env var contract.

### Step 7: Open a pull request

- [ ] Validate passes: `platformctl tenants validate tenants/`
- [ ] Open pull request; CI validation must pass
- [ ] Merge to `main`
- [ ] The governance ApplicationSet detects the new `tenant.yaml` and
      generates all resources within ~3 minutes

### Step 8: Verify

- [ ] `platformctl bootstrap verify` reports all gates green
- [ ] ArgoCD Application `governance-<tenant>` shows Synced/Healthy
- [ ] (Only if ARC enabled) Pods running in `<tenant>-runners` namespace
- [ ] (Only if ARC enabled) Runner appears in GitHub org Settings > Actions > Runners
- [ ] If deployment repo configured: ArgoCD Applications `<tenant>-<name>` show Synced/Healthy

## Offboarding a Tenant

See [TENANT-MODEL.md §Removing a tenant](TENANT-MODEL.md#removing-a-tenant).
