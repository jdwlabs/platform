# Database Restore Guide

Step-by-step guide for restoring a PostgreSQL cluster from a SQL dump (`.sql.gz`).
This is the only restore path that exists: there is no WAL archive and no CNPG
`Backup` to recover from, so there is no point-in-time recovery — see
[OPERATIONS.md §3, Backups](OPERATIONS.md#backups).

---

## When to use this guide

Use this guide to restore one or more databases from a `pg_dumpall`-format
`.sql.gz` made by the `postgres-backup` or `litellm-db-backup` CronJob. The
data comes back as of the dump's timestamp; anything written after it is lost.

---

## Prerequisites

- `kubectl` configured with cluster access
- Identify the target cluster:
  - **Production:** `platform-postgresql-cluster-prd` in namespace `database`,
    dumps under `gdrive:postgres-backups/prd-*.sql.gz`
  - **LiteLLM gateway:** `platform-litellm-db-cluster` in namespace `ai-sre`,
    dumps under `gdrive:postgres-backups/litellm/litellm-*.sql.gz`
  - **Non-prod:** `platform-postgresql-cluster-non` in namespace `database` (not
    backed up; only useful with a dump you made yourself)

The commands below use the prd cluster and pod names; substitute the namespace,
cluster and pod for the LiteLLM cluster.

---

## Step 0 — Fetch the dump from Google Drive

The Drive credentials live only in the `rclone-gdrive` Secret, so the dump is
fetched by a short-lived pod that mounts it, and streamed straight into the
primary's `/dev/shm` without touching your machine's disk. Nothing here prints
the credential. Create the pod in the namespace of the cluster you are
restoring (`database` or `ai-sre`; both carry the Secret):

```bash
NS=database
kubectl -n "$NS" apply -f - <<'EOF'
apiVersion: v1
kind: Pod
metadata:
  name: dump-fetch
spec:
  restartPolicy: Never
  activeDeadlineSeconds: 3600
  automountServiceAccountToken: false
  securityContext:
    runAsNonRoot: true
    runAsUser: 999
    seccompProfile:
      type: RuntimeDefault
  containers:
    - name: rclone
      image: rclone/rclone:1.74.4@sha256:c61954aaa32328a5486715dd063a81c7879f5195ad3505cd362deddd509dc4a1
      env:
        # rclone rewrites its config when it refreshes the token; the Secret
        # mount is read-only.
        - name: RCLONE_CONFIG
          value: /tmp/rclone.conf
      command: ["/bin/sh", "-c", "cp /rclone-config/rclone.conf /tmp/rclone.conf && sleep 3600"]
      securityContext:
        allowPrivilegeEscalation: false
        readOnlyRootFilesystem: true
        capabilities:
          drop: ["ALL"]
      volumeMounts:
        - { name: rclone-config, mountPath: /rclone-config, readOnly: true }
        - { name: tmp, mountPath: /tmp }
  volumes:
    - name: rclone-config
      secret:
        secretName: rclone-gdrive
    - name: tmp
      emptyDir: {}
EOF
kubectl -n "$NS" wait --for=condition=Ready pod/dump-fetch --timeout=120s
```

List the dumps and pick one. Names are UTC start times; the newest is last:

```bash
kubectl -n "$NS" exec dump-fetch -- rclone lsl gdrive:postgres-backups \
  --max-depth 1 --include 'prd-*.sql.gz' | sort -k2,3
# LiteLLM: gdrive:postgres-backups/litellm --include 'litellm-*.sql.gz'
```

Stream it into the primary and check it arrived whole — the size must match
the `lsl` listing and `gzip -t` must pass. The primary moves on failover, so
read its name rather than assuming one:

```bash
DUMP=prd-20261127T040859Z.sql.gz
PRIMARY=$(kubectl -n "$NS" get cluster platform-postgresql-cluster-prd -o jsonpath='{.status.currentPrimary}')
kubectl -n "$NS" exec dump-fetch -- rclone cat "gdrive:postgres-backups/$DUMP" \
  | kubectl -n "$NS" exec -i "$PRIMARY" -c postgres -- \
      sh -c 'cat > /dev/shm/restore.sql.gz && stat -c %s /dev/shm/restore.sql.gz && gzip -t /dev/shm/restore.sql.gz && echo OK'
kubectl -n "$NS" delete pod dump-fetch
```

Rclone prints a `NOTICE` about its shared client_id on every command; it goes
to stderr and does not corrupt the stream (see OPERATIONS.md §3.1).

To keep a local copy as well, write the stream to a file instead
(`... rclone cat ... > "$DUMP"`) and copy it in with Step 2.

---

## Step 1 — Identify the primary pod

```bash
kubectl get cluster -n database
```

Look for the pod listed in the `PRIMARY` column (e.g. `platform-postgresql-cluster-prd-1`).
All restore commands run against the primary.

---

## Step 2 — Copy the dump into the pod

Skip this if Step 0 streamed the dump into the pod already.

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

The `\restrict` / `\unrestrict` lines are written by `pg_dump` itself (from
PostgreSQL 17.6 / 16.10): they stop a tampered dump from running psql
meta-commands. The cluster's `psql` understands them; an older `psql` would
reject them as invalid commands and carry on.

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
| `ERROR: unrecognized configuration parameter "transaction_timeout"` | Dump was made with PG17, cluster runs PG16 — harmless (one per database) |

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

For the non-prod cluster, replace `prd` with `non` in the secret name. For the
LiteLLM cluster the secret is `platform-litellm-db-cluster-app` in `ai-sre`.

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
[ ] 0. Fetch the dump from Drive into /dev/shm (dump-fetch pod), check size + gzip -t
[ ] 1. Identify primary pod
[ ] 2. (Only for a local file) copy .sql.gz to /dev/shm in the primary pod
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
| `\restrict` / `\unrestrict` lines | Written by `pg_dump` 17.6+/16.10+ as a guard against tampered dumps. Understood by the cluster's `psql`; an older client reports them as invalid commands and continues |
| PG17 dump → PG16 cluster | `transaction_timeout` parameter warnings are harmless |
| `app` password always overwritten | The dump stores a hashed password at backup time. Step 8 is always required |
