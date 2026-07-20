# Memory Efficiency — Phase 1 Baseline

Measured foundation for the cluster memory right-sizing effort. Captured live 2026-07-20; re-run the methodology in [01-baseline.md](01-baseline.md) to refresh each cycle.

1. [01-baseline.md](01-baseline.md) — cluster/node/namespace requests vs live usage, structural findings
2. [02-qos-offenders.md](02-qos-offenders.md) — BestEffort / missing-limit / missing-request inventory with GitOps source paths
3. [03-chart-inventory.md](03-chart-inventory.md) — per-chart reserves vs usage, oversized flags, keep/trim/replace verdicts
4. [04-hit-list.md](04-hit-list.md) — ranked actions, the numeric epic target, Phase 2 mapping (**start here for what to do next**)
5. [appendix-workloads.md](appendix-workloads.md) — full per-workload table
