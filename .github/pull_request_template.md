## What

<!-- One sentence: what changed and why -->

## Type of change

- [ ] `feat` — new feature or capability
- [ ] `fix` — bug fix
- [ ] `chore` — maintenance / dependency / config
- [ ] `docs` — documentation only
- [ ] `ci` — CI/CD pipeline change
- [ ] `refactor` — restructure, no behavior change
- [ ] `test` — test additions or updates
- [ ] `perf` — performance improvement

## Checklist

- [ ] PR title follows conventional commit format: `type(scope): description`
- [ ] `platformctl tenants validate` passes (if tenant config changed)
- [ ] `yamllint tenants/ bootstrap/` passes (if YAML changed)
- [ ] `cd cli && go test ./...` passes (if CLI changed)
- [ ] No secrets or credentials in diff
