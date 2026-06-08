# Contributing

## Commit Convention

This repository follows [Conventional Commits](https://www.conventionalcommits.org/en/v1.0.0/).

### Types

| Type | When to use |
|------|-------------|
| `feat` | New feature or capability (new platformctl command, new platform service) |
| `fix` | Bug fix |
| `build` | Build system or dependency change (Go modules, chart deps) |
| `chore` | Maintenance: config, tooling (no production code change) |
| `ci` | CI/CD pipeline changes |
| `docs` | Documentation only (no code changes) |
| `perf` | Performance improvement |
| `refactor` | Code restructure with no behavior change |
| `revert` | Reverting a previous commit |
| `style` | Formatting or whitespace only (no logic change) |
| `test` | Adding or updating tests |

### Format

```
<type>[optional scope]: <description>

[optional body]

[optional footer(s)]
```

### Examples

```
feat(platformctl): add heal --stuck-sync subcommand
fix(tenant): correct RBAC namespace selector for ARC runners
ci: add kubeconform validation to PR workflow
docs: update BOOTSTRAP.md with phase 4 secret seeding steps
chore: upgrade cert-manager chart to 1.17.0
```

### Footers

Footers appear after an optional body, separated by a blank line. Common footers:

| Footer | When to use |
|--------|-------------|
| `Refs: JDWLABS-XX` | Links commit to a Jira issue (does not close it) |
| `Closes: JDWLABS-XX` | Closes the Jira issue on merge |
| `Closes: #N` | Closes a GitHub issue by number |
| `BREAKING CHANGE: <desc>` | Required when a commit introduces a breaking platformctl interface change |
| `Co-Authored-By: Name <email>` | Credit a co-author (human or AI) |

**AI contributor footer** — include when commits were written with AI assistance:

```
Co-Authored-By: Claude Sonnet 4.6 <noreply@anthropic.com>
```

**Full examples with footers:**

```
feat(platformctl): add heal --stuck-sync subcommand

Terminates an ArgoCD sync that has hung due to a Helm hook Job
TTL race. Idempotent — safe to re-run.

Refs: JDWLABS-55
Co-Authored-By: Claude Sonnet 4.6 <noreply@anthropic.com>
```

```
fix!(platformctl): rename --dry-run to --plan across all subcommands

BREAKING CHANGE: --dry-run flag removed; use --plan instead.
Scripts calling platformctl with --dry-run must be updated.

Closes: JDWLABS-61
```

### Rules

- Subject line ≤72 characters, lowercase, no trailing period
- Use imperative mood: "add" not "added" / "adds"
- Breaking changes: add `!` after type/scope and a `BREAKING CHANGE:` footer

## Pull Requests

1. Branch from `main`: `git checkout -b feat/short-description`
2. Validate before opening PR: `platformctl tenants validate && yamllint tenants/ bootstrap/`
3. PR title must follow conventional commit format
4. Squash-merge to main

## Development Setup

```bash
cd cli && go build ./...           # Build platformctl
cd cli && go test ./...            # Run tests
yamllint tenants/ bootstrap/       # Validate YAML
kubeconform                        # Validate Kubernetes manifests
```
