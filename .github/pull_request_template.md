## What

<!-- One sentence: what changed and why -->

## Type of change

- [ ] `feat` — new feature or capability
- [ ] `fix` — bug fix
- [ ] `build` — build system or external dependency change
- [ ] `chore` — maintenance / config / tooling
- [ ] `ci` — CI/CD pipeline change
- [ ] `docs` — documentation only
- [ ] `perf` — performance improvement
- [ ] `refactor` — restructure, no behavior change
- [ ] `revert` — revert a previous commit
- [ ] `style` — formatting / whitespace (no logic change)
- [ ] `test` — test additions or updates

## Checklist

- [ ] PR title follows conventional commit format: `type(scope): description`
- [ ] `platformctl tenants validate` passes (if tenant config changed)
- [ ] `yamllint tenants/ bootstrap/` passes (if YAML changed)
- [ ] `cd cli && go test ./...` passes (if CLI changed)
- [ ] No secrets or credentials in diff
