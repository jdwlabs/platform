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
| `Refs: JDWLABS-<n>` | Links commit to a Jira issue (does not close it) |
| `Closes: JDWLABS-<n>` | Closes the Jira issue on merge |
| `Closes: #N` | Closes a GitHub issue by number |
| `BREAKING CHANGE: <desc>` | Required when a commit introduces a breaking platformctl interface change |
| `Co-Authored-By: Name <email>` | Credit a co-author (human or AI) |
| `Assisted-by: <agent>:<model-id>` | Name the AI agent and model (required alongside an AI `Co-Authored-By`) |

**AI attribution trailers** — required on every AI-assisted commit, naming the
agent and the model that actually ran (never copy a model from an example):

```
Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
Assisted-by: Claude Code:claude-opus-5-5
```

Codex: `Co-Authored-By: Codex <codex@openai.com>` plus `Assisted-by: Codex:<model-id>`.
Attribution lives only in commits — PR titles, bodies and comments carry no
"Generated with" footer or attribution line.

**Full examples with footers:**

```
feat(platformctl): add heal --stuck-sync subcommand

Terminates an ArgoCD sync that has hung due to a Helm hook Job
TTL race. Idempotent — safe to re-run.

Refs: JDWLABS-<n>
Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
Assisted-by: Claude Code:claude-opus-5-5
```

```
fix!(platformctl): rename --dry-run to --plan across all subcommands

BREAKING CHANGE: --dry-run flag removed; use --plan instead.
Scripts calling platformctl with --dry-run must be updated.

Closes: JDWLABS-<n>
```

### Rules

- Subject line ≤72 characters, lowercase, no trailing period
- Use imperative mood: "add" not "added" / "adds"
- Breaking changes: add `!` after type/scope and a `BREAKING CHANGE:` footer

## Pull Requests

1. Branch from `main`: `git checkout -b feat/short-description`
2. Validate before opening PR: `platformctl tenants validate && yamllint tenants/ bootstrap/`
3. PR title must follow conventional commit format, under 70 characters
4. Body follows `.github/pull_request_template.md`: keep only sections with
   content, ~150 words, written for the reviewer
5. Rebase-merge to main (squash and merge commits are disabled). Every commit
   lands on `main` as-is, so make each one a single logical change before merging

## Development Setup

```bash
cd cli && go build ./...           # Build platformctl
cd cli && go test ./...            # Run tests
yamllint tenants/ bootstrap/       # Validate YAML
kubeconform                        # Validate Kubernetes manifests
```
