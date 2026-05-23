# Database Restore Guide

Step-by-step guide for restoring a PostgreSQL cluster from a SQL dump (`.sql.gz`).
For CNPG declarative recovery (WAL/snapshot restore), see [OPERATIONS.md §3](OPERATIONS.md#3-postgresql-operations).

---

## When to use this guide

Use this guide when you have a `pg_dumpall`-format `.sql.gz` file (e.g. from the
`postgres-backup` CronJob) and need to restore one or more databases to the live
CNPG cluster.

> **Not a CNPG snapshot restore.** CNPG's native WAL-based recovery requires
> editing the `Cluster` CR. This guide covers plain SQL dump restores only.

---

## Prerequisites

- `kubectl` configured with cluster access
- The `.sql.gz` dump file on your local machine
- Identify the target cluster:
  - **Production:** `platform-postgresql-cluster-prd` in namespace `database`
  - **Non-prod:** `platform-postgresql-cluster-non` in namespace `database`

---

## Step 1 — Identify the primary pod

```bash
kubectl get cluster -n database
```

Look for the pod listed in the `PRIMARY` column (e.g. `platform-postgresql-cluster-prd-1`).
All restore commands run against the primary.

---

## Step 2 — Copy the dump into the pod

CNPG pods have a **read-only root filesystem**. The only writable location available
without mounting a volume is `/dev/shm` (an in-memory tmpfs, ~14 GB).

**Windows (PowerShell):**
```powershell
# Must cd first — kubectl cp misreads "C:/" as "host:path"
Push-Location "C:\path\to\dumps"
kubectl cp ".\your-dump.sql.gz" platform-postgresql-cluster-prd-1:/dev/shm/restore.sql.gz `
  -n database -c postgres
Pop-Location
```

**Linux / macOS:**
```bash
kubectl cp ./your-dump.sql.gz platform-postgresql-cluster-prd-1:/dev/shm/restore.sql.gz \
  -n database -c postgres
```

Verify the copy:
```bash
kubectl exec platform-postgresql-cluster-prd-1 -n database -c postgres -- \
  ls -lh /dev/shm/restore.sql.gz
```

---

## Step 3 — Inspect the dump

Peek at the header to confirm it is a `pg_dumpall` cluster dump:

```bash
kubectl exec platform-postgresql-cluster-prd-1 -n database -c postgres -- \
  sh -c "gunzip -c /dev/shm/restore.sql.gz | head -5"
```

Expected output:
```
--
-- PostgreSQL database cluster dump
--

\restrict <token>
```

The `\restrict` / `\unrestrict` tokens are CNPG barman plugin markers. Standard
`psql` does not know these commands and will warn about them but continue safely.

Find the `\connect` line numbers — you need these if a partial re-restore is required:

```bash
kubectl exec platform-postgresql-cluster-prd-1 -n database -c postgres -- \
  sh -c "gunzip -c /dev/shm/restore.sql.gz | grep -n '\\\\connect '"
```

Example output:
```
99:\connect template1
193:\connect app
249:\connect dotablazetech_prd
622:\connect jdwlabs_prd
1237:\connect postgres
```

Note the start line for each database you care about. The end line for database X
is one line before the start line of the next `\connect`.

---

## Step 4 — Terminate active connections

Drop all app connections to the target databases before restoring. If you skip this,
the `DROP DATABASE` in the dump will fail and leave the database in a mixed state.

```bash
kubectl exec platform-postgresql-cluster-prd-1 -n database -c postgres -- \
  psql -U postgres -c "
    SELECT pg_terminate_backend(pid)
    FROM pg_stat_activity
    WHERE datname IN ('jdwlabs_prd', 'dotablazetech_prd')
      AND pid <> pg_backend_pid();"
```

Adjust the `IN (...)` list to match the databases you are restoring.

> **Tip:** Scale down any applications that connect to these databases before this
> step so connections do not reappear between termination and the DROP:
> ```bash
> kubectl scale deployment <app> -n <namespace> --replicas=0
> ```

---

## Step 5 — Run the full restore

```bash
kubectl exec platform-postgresql-cluster-prd-1 -n database -c postgres -- \
  sh -c "gunzip -c /dev/shm/restore.sql.gz | psql -U postgres --set ON_ERROR_STOP=off 2>&1"
```

### Expected (harmless) errors

| Error | Why it's safe |
|---|---|
| `ERROR: role "app" cannot be dropped` | CNPG-managed role in use — dump skips it |
| `ERROR: current user cannot be dropped` | Cannot drop `postgres` while connected as it |
| `ERROR: role "streaming_replica" cannot be dropped` | CNPG replication role in use |
| `ERROR: role "app/postgres/streaming_replica" already exists` | Roles survive — `ALTER ROLE` that follows still applies |
| `ERROR: unrecognized configuration parameter "transaction_timeout"` | Dump was made with PG17, cluster runs PG16 — harmless |

### Errors that need action

| Error | What to do |
|---|---|
| `ERROR: database "X" is being accessed by other users` | Step 4 did not fully clear connections. Follow [Step 6](#step-6--recover-from-a-failed-database-drop). |
| `ERROR: relation "X" already exists` + duplicate key errors on same DB | Consequence of the failed DROP above. Follow [Step 6](#step-6--recover-from-a-failed-database-drop). |

---

## Step 6 — Recover from a failed database drop

If a database could not be dropped during Step 5, its tables will be in a mixed
state (partially restored). Fix it by dropping and restoring that database
individually.

```bash
# 1. Terminate connections again
kubectl exec platform-postgresql-cluster-prd-1 -n database -c postgres -- \
  psql -U postgres -c "
    SELECT pg_terminate_backend(pid) FROM pg_stat_activity
    WHERE datname = 'dotablazetech_prd' AND pid <> pg_backend_pid();"

# 2. Drop and recreate (must be separate commands — DROP DATABASE cannot run in a transaction)
kubectl exec platform-postgresql-cluster-prd-1 -n database -c postgres -- \
  psql -U postgres -c "DROP DATABASE IF EXISTS dotablazetech_prd;"

kubectl exec platform-postgresql-cluster-prd-1 -n database -c postgres -- \
  psql -U postgres -c "CREATE DATABASE dotablazetech_prd OWNER app;"

# 3. Restore only that database's section using the line numbers from Step 3
#    (start=249, end=621 in this example — adjust to your dump)
kubectl exec platform-postgresql-cluster-prd-1 -n database -c postgres -- \
  sh -c "gunzip -c /dev/shm/restore.sql.gz | sed -n '249,621p' | psql -U postgres -d dotablazetech_prd"
```

---

## Step 7 — Fix table ownership

`pg_dumpall` runs as the `postgres` superuser. Tables are created owned by
`postgres`, and the `ALTER TABLE ... OWNER TO app` statements follow later — but
if the dump section is piped into a specific `psql -d <db>` call (Step 6), those
owner fixups may apply to a different database context.

Run this in every restored database to normalise ownership:

```bash
kubectl exec platform-postgresql-cluster-prd-1 -n database -c postgres -- \
  psql -U postgres -d dotablazetech_prd -c "
DO \$\$ DECLARE r RECORD; BEGIN
  FOR r IN
    SELECT schemaname, tablename FROM pg_tables
    WHERE tableowner = 'postgres'
      AND schemaname NOT IN ('pg_catalog', 'information_schema')
  LOOP
    EXECUTE 'ALTER TABLE ' || quote_ident(r.schemaname) || '.' || quote_ident(r.tablename) || ' OWNER TO app';
  END LOOP;
END; \$\$;"
```

Repeat for each restored database (change `-d dotablazetech_prd` accordingly).

---

## Step 8 — Fix the `app` role password

**Always do this after any SQL dump restore.**

The dump includes `ALTER ROLE app WITH PASSWORD '<hash-from-backup-date>'`. This
overwrites the live password with the one from the day the dump was made.
Applications will start failing with `password authentication failed for user "app"`.

Restore the correct password from the CNPG-managed Kubernetes secret:

```bash
APP_PASS=$(kubectl get secret platform-postgresql-cluster-prd-app \
  -n database -o jsonpath='{.data.password}' | base64 -d)

kubectl exec platform-postgresql-cluster-prd-1 -n database -c postgres -- \
  psql -U postgres -c "ALTER ROLE app WITH PASSWORD '$APP_PASS';"
```

For the non-prod cluster, replace `prd` with `non` in the secret name.

---

## Step 9 — Restart applications

Scale deployments back up (if you scaled them down in Step 4), or do a rollout
restart to force reconnection with the now-correct credentials:

```bash
kubectl rollout restart deployment/<app-name> -n <namespace>

# Monitor until healthy
kubectl rollout status deployment/<app-name> -n <namespace>
```

Verify the app logs show a successful DB connection before moving on.

---

## Step 10 — Clean up

Remove the dump from the pod's memory filesystem:

```bash
kubectl exec platform-postgresql-cluster-prd-1 -n database -c postgres -- \
  sh -c "rm /dev/shm/restore.sql.gz"
```

---

## Quick reference checklist

```
[ ] 1. Identify primary pod
[ ] 2. Copy .sql.gz to /dev/shm in the primary pod
[ ] 3. Inspect dump header + note \connect line numbers
[ ] 4. Terminate connections (scale down apps if needed)
[ ] 5. Run full restore with --set ON_ERROR_STOP=off
[ ] 6. If any DB drop failed → manual drop/recreate + section restore
[ ] 7. Fix table ownership (ALTER TABLE ... OWNER TO app)
[ ] 8. Fix app role password from CNPG secret  ← easy to forget
[ ] 9. Restart / scale up applications
[ ] 10. Clean up /dev/shm
```

---

## Known quirks

| Quirk | Detail |
|---|---|
| `kubectl cp` Windows path parsing | `C:/path` is read as `host:path`. Always `cd` to the folder and use `.\filename` |
| CNPG read-only filesystem | Only `/dev/shm` and the data PVC (`/var/lib/postgresql/data`) are writable. Use `/dev/shm` for temp files |
| `\restrict` / `\unrestrict` tokens | CNPG-specific psql extensions. Standard `psql` ignores them with a warning — safe to ignore |
| PG17 dump → PG16 cluster | `transaction_timeout` parameter warnings are harmless |
| `app` password always overwritten | The dump stores a hashed password at backup time. Step 8 is always required |
