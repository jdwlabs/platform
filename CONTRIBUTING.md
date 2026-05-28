# Contributing

## Commit Convention

This repository follows [Conventional Commits](https://www.conventionalcommits.org/en/v1.0.0/).

### Types

| Type | When to use |
|------|-------------|
| `feat` | New feature or capability (new platformctl command, new platform service) |
| `fix` | Bug fix |
| `chore` | Maintenance: dependency upgrades, config, tooling |
| `docs` | Documentation only (no code changes) |
| `ci` | CI/CD pipeline changes |
| `refactor` | Code restructure with no behavior change |
| `test` | Adding or updating tests |
| `perf` | Performance improvement |
| `revert` | Reverting a previous commit |

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
