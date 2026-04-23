# AI Agents & OpenClaw

This document tracks the integration of AI agents and the OpenClaw "Brain" backend within the Jdwlabs platform.

## Architecture

OpenClaw is deployed as a tenant-level service within the `jdwlabs-ai` namespace. It provides an autonomous agent runtime that can execute shell commands, browse the web, and interact with Kubernetes.

- **Namespace:** `jdwlabs-ai`
- **Service Name:** `openclaw`
- **Internal URL:** `http://openclaw.jdwlabs-ai.svc.cluster.local:18789`
- **Public URL:** `https://ai.jdwlabs.com` (via Gateway API)
- **Persistence:** Longhorn (5Gi PVC)
- **Security:** Privileged namespace profile (required for BuildKit/Browser sidecars)

## Components

### OpenClaw Gateway
The core backend service. 
- **Chart:** `openclaw`
- **Repo:** `https://chrisbattarbee.github.io/openclaw-helm`
- **Documentation:** [openclaw.ai/docs](https://openclaw.ai/docs)

### Chromium Sidecar
A headless browser sidecar that allows agents to use web-browsing skills. Enabled via `chromium.enabled: true` in `values.yaml`.

## Manual Setup (Vault)

OpenClaw requires API keys for LLM providers. These are managed via `ExternalSecret` and must be manually added to Vault.

1.  **Path:** `kv/jdwlabs/jdwlabs-ai-keys`
2.  **Keys:**
    - `anthropic_api_key`: (sk-ant-...)
    - `openai_api_key`: (sk-...)

## Troubleshooting

### Connectivity
If the UI is not accessible via the public URL, verify the `HTTPRoute`:
```bash
kubectl describe httproute openclaw -n jdwlabs-ai
```

### Logs
Check agent execution logs:
```bash
kubectl logs -n jdwlabs-ai -l app.kubernetes.io/name=openclaw -c openclaw
```

### Persistence
Verify the workspace volume:
```bash
kubectl get pvc -n jdwlabs-ai
```
