# platformctl CLI Uplift — Design Spec

**Date:** 2026-05-17  
**Status:** Approved  
**Approach:** B — Inspired port with zap, tailored to platformctl phase model

## Goal

Uplift platformctl's human-facing output to match the infrastructure CLI quality:
colored zap logging, box-drawing phase summary UI, run sessions with log files.
The `--json` machine-parseable contract is preserved unchanged.

## Reference

Infrastructure CLI logging: `../infrastructure/bootstrap/internal/logging/`  
- `logger.go` — RunSession, zap tee core, kvEncoder  
- `banner.go` — Box type, PrintBanner, box-drawing helpers  
- `summary.go` — SUMMARY.txt writer

## Package Structure

New package `cli/internal/display/`:

```
cli/internal/display/
  banner.go      — PrintBanner (PLATFORM ASCII art)
  box.go         — Box{}: Header/Section/Row/Item/Badge/Footer + helpers
  encoder.go     — kvEncoder: zap console encoder → key=value output
  session.go     — RunSession: tee core, 3 log files, lifecycle
  summary.go     — SUMMARY.txt + runs.log writer
  audit.go       — AuditLogger
```

Ported from infra CLI with platformctl-specific fields (no ClusterName,
TerraformTFVars, node counts).

## RunSession

```go
type RunSession struct {
    RunDir      string
    StartTime   time.Time
    Logger      *zap.Logger
    AuditLog    *AuditLogger
    Console     io.Writer   // tees stderr + console.log
    ConsoleFile io.Writer   // console.log only
    NoColor     bool
    Command     string      // "bootstrap", "heal", "tenants"
    closers     []io.Closer
    runsLogDir  string

    // outcome counters set during execution
    PhasesRun    int
    PhasesFailed int
}
```

### Log location

`$HOME/.platformctl/runs/YYYY-MM-DD/run-YYYYMMDD_HHMMSS/`

Three files per run:
- `console.log` — colored terminal output (same as stderr)
- `structured.log` — JSON (one zap log object per line)
- `audit.log` — command, flags, timestamps, exit code

Global registry files:
- `~/.platformctl/runs/runs.log` — one line per run: `timestamp | command | rundir | status`
- `~/.platformctl/runs/latest.txt` — path to most recent run dir

### Config

```go
type Config struct {
    LogDir  string // default: $HOME/.platformctl/runs
    Command string
    NoColor bool
}
```

Override via `PLATFORMCTL_LOG_DIR` env var (future; not in initial implementation).

## Logging Format

zap tee core fans out to two sinks:

1. **stderr + console.log**: colored key=value (kvEncoder)
2. **structured.log**: JSON encoder

`audit.log` is written by a separate `AuditLogger` via explicit `AuditLog.Record()` calls —
not a zap core. Records command invocation, flags, and exit code.

Console output format (colored):
```
15:04:05 INFO  argocd-install  phase=bootstrap state=not_started
15:04:05 INFO  argocd-install  phase=bootstrap state=applying
15:04:05 OK    argocd-install  phase=bootstrap state=applied_and_verified
15:04:05 FAIL  argocd-install  phase=bootstrap error="argocd-server not Available"
```

Level colors (ANSI):
- `OK` / `INFO` → green
- `WAIT` / progressing → yellow
- `FAIL` / `ERROR` → red
- `BROKEN` → white on red background
- `DEBUG` → blue
- timestamp → dim

## Banner

Printed to stderr at startup (non-`--json` only):

```
██████╗ ██╗      █████╗ ████████╗███████╗ ██████╗ ██████╗ ███╗   ███╗
██╔══██╗██║     ██╔══██╗╚══██╔══╝██╔════╝██╔═══██╗██╔══██╗████╗ ████║
██████╔╝██║     ███████║   ██║   █████╗  ██║   ██║██████╔╝██╔████╔██║
██╔═══╝ ██║     ██╔══██║   ██║   ██╔══╝  ██║   ██║██╔══██╗██║╚██╔╝██║
██║     ███████╗██║  ██║   ██║   ██║     ╚██████╔╝██║  ██║██║ ╚═╝ ██║
╚═╝     ╚══════╝╚═╝  ╚═╝   ╚═╝   ╚═╝      ╚═════╝ ╚═╝  ╚═╝╚═╝     ╚═╝
━━━ Platform Bootstrap Tool v0.1.0 ━━━
```

## End-of-Run Box Summary

Printed via `Box` to stderr/console on command exit (non-`--json` only).
Box width: 63 chars (matching infra CLI).

```
┏━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━┓
┃ BOOTSTRAP SUMMARY                                            ┃
┣━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━┫
┃  Status:    SUCCESS                                          ┃
┃  Duration:  4m32s                                            ┃
┃  Phases:    5/5                                              ┃
┠─────────────────────────────────────────────────────────────┨
┃  ✓  Phase 1  argocd-install                                  ┃
┃  ✓  Phase 2  root-apply                                      ┃
┃  ✓  Phase 3  vault-init                                      ┃
┃  ✓  Phase 4  vault-seed                                      ┃
┃  ✗  Phase 5  backups-init — cronjob not found                ┃
┗━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━┛
Run log: ~/.platformctl/runs/2026-05-17/run-20260517_020637/
```

`writeLine` auto-pads content to inner width (61 chars) using
`utf8.RuneCountInString`, with ANSI-aware truncation/wrap for long lines.

## SUMMARY.txt

Written to run dir on `Session.Close(exitErr)`:

```
platformctl run summary
========================
Command:   bootstrap phase 1
Started:   2026-05-17 02:06:37 UTC
Duration:  4m32s
Status:    FAILED

Phases run:    1
Phases failed: 1

  ✗  argocd-install — argocd-server not Available (timeout 10m)

Log dir: ~/.platformctl/runs/2026-05-17/run-20260517_020637/
  console.log    — colored terminal output
  structured.log — JSON events (one per line)
  audit.log      — commands and flags invoked
```

## Integration: root.go

`Globals` gains `Session *display.RunSession` (nil when `--json`).

`PersistentPreRunE`: create session, print banner, record audit entry.  
`PersistentPostRunE`: call `session.Close(g.runErr)`, print box summary.

Cobra does not pass `RunE`'s error to `PersistentPostRunE`. Store it in `Globals`:

```go
type Globals struct {
    JSON           bool
    Branch         string
    NonInteractive bool
    NoColor        bool
    Session        *display.RunSession // nil when --json
    runErr         error               // set by RunE shim; read by PersistentPostRunE
}
```

## Integration: events.go

`Emitter` gains optional `session *display.RunSession` field.

When session present: `Emit` delegates to `session.Logger` (zap).  
When `--json`: existing JSON path unchanged — raw JSON to stdout.  
When neither: existing `[status] phase/name GLYPH — message` path (fallback/test).

The `Event` struct and JSON schema are frozen — no changes.

## Phase 1 Timeout Fix

`cli/internal/bootstrap/phase1_argocd.go:60`: increase verify timeout
`5*time.Minute` → `10*time.Minute`.

Fresh cluster cold image pulls routinely exceed 5 minutes.

## Dependencies

Add to `cli/go.mod`:
- `go.uber.org/zap` (already used in infra CLI; well-proven)

## Testing

- `display/` package: unit tests for `Box`, `kvEncoder`, `RunSession`
  (use `io.Pipe` / temp dirs; no cluster needed)
- `Emitter` tests: verify `--json` path unchanged, session path delegates to zap
- Integration: existing bootstrap/cascade tests unaffected (they use `FakeRunner`)
