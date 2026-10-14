# ADR: TrueNAS SMART health comes from alert.list, not a results API

Status: accepted, 2026-10-14. Supersedes one decision in
[truenas-metrics-what-the-graphite-push-can-and-cannot-carry](0025-truenas-metrics-what-the-graphite-push-can-and-cannot-carry.md):
that the API-polling exporter reads SMART self-test results through
`smart.test.results.query`. Everything else in that record stands.

## Context

TrueNAS 25.10.4 removed `smart.test.results.query` along with the SMART test
UI, so the `smart_test` sub-collector failed on every scrape with rpc error
`-32601: Method does not exist`, `truenas_disk_smart_test_passed` was never exported, and
`TrueNASDiskSmartTestFailed` could not fire. Nothing alerted on a
sub-collector failing, which is how it went unnoticed.

Read from `truenas/middleware` at tag `release/25.10.4`, not from the NAS:

- `plugins/disk_/smart.py` exposes no public SMART results method. The NAS's
  own SMART check (`alert/source/smart.py`, every 90 minutes) raises
  `SMARTUncorrectedErrors`, `SMARTFailedSelfTest`, `SMARTEraseCycleCount`
  (WARNING) and `SMARTSpareBlockCount` (CRITICAL). The class string is the
  Python class name minus `AlertClass` (`alert/base.py`, `AlertClassMeta`).
- `alert.list` takes no arguments, requires `ALERT_LIST_READ`, returns
  dismissed alerts as well as active ones, and reports each alert's level
  after any per-class override set in the TrueNAS UI.
- The built-in Read-Only Administrator privilege grants role
  `READONLY_ADMIN` (alembic `2024-01-15_20-10_builtin_roles_groups.py`), which
  `role.py` composes from every role ending `_READ`, `ALERT_LIST_READ`
  included. Whether `prometheus-readonly` still holds that privilege is a
  NAS-side fact this was not checked against.

## Decision

The `smart_test` sub-collector is replaced by `alerts`, exporting
`truenas_alert_active{klass,level}`: a count of non-dismissed alerts per class
and level. Alert text, args and serials are not exported; they would make each
alert its own series, and the TrueNAS alert list already holds them.
`TrueNASDiskSmartTestFailed` is replaced by `TrueNASDiskSMARTWarning` and
`TrueNASDiskSMARTCritical`, `TrueNASAlertCritical` forwards any other alert
the NAS rates CRITICAL or above, and `TrueNASExporterCollectorFailing` fires
when any sub-collector reports 0 for 15m. Verified with Go unit tests against
a fake shaped like the 25.10.4 `Alert` API model and with promtool rule tests;
not yet against the live NAS.
