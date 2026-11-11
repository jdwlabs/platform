#!/usr/bin/env bash
# Create the MinIO buckets, policies and users for CNPG WAL archiving, and write
# each user's credentials straight into Vault. Run it in your own terminal on
# the devbox, outside any agent session. Nothing here prints a secret value.
#
#   bash scripts/cnpg-minio-setup.sh            # both clusters
#   bash scripts/cnpg-minio-setup.sh prd        # one of: prd non
#   ROTATE=1 bash scripts/cnpg-minio-setup.sh prd
#
# Per cluster (prd, non):
#   bucket cnpg-<env>, policy cnpg-<env> reaching only that bucket, user
#   cnpg-<env> with a freshly generated secret, Vault kv/cnpg-<env>-s3
#   {ACCESS_KEY_ID, ACCESS_SECRET_KEY}. Separate buckets and keys so the
#   non-prd cluster can never delete prd's archive.
#
# Order matters on a rotation: Vault reachability is checked before MinIO is
# touched, the new secret goes to Vault immediately after MinIO accepts it, and
# the cluster's ExternalSecret is force-synced, so archiving is broken only for
# the seconds in between (well inside the 15-minute alert).
#
# Re-running is safe: buckets and policies are idempotent, and an existing user
# is left alone unless ROTATE=1. If Vault is ever rebuilt, re-run with ROTATE=1:
# kv/cnpg-*-s3 is not part of `platformctl bootstrap seed`.
set -euo pipefail
umask 077

MINIO_URL=https://192.168.1.205:9000
VAULT_MOUNT=kv
CA_FILE=${CA_FILE:-$HOME/projects/jdwlabs/infrastructure/terraform/minio-ca.crt}
MC_MODULE=github.com/minio/mc@v0.0.0-20251106162529-77f82e18b540
MC_CACHE=$HOME/.cache/jdwlabs-mc
ALL_ENVS=(prd non)

die() { echo "ERROR: $*" >&2; exit 1; }
step() { echo "==> $*"; }

if [ $# -eq 0 ]; then
  envs=("${ALL_ENVS[@]}")
else
  envs=("$@")
  for e in "${envs[@]}"; do
    case "$e" in prd|non) ;; *) die "unknown cluster '$e' (expected: prd non)" ;; esac
  done
fi

for bin in kubectl vault openssl sha256sum go curl python3 base64; do
  command -v "$bin" >/dev/null || die "$bin not found on PATH"
done
[ -r "$CA_FILE" ] || die "MinIO CA not readable at $CA_FILE (set CA_FILE=...)"

work=$(mktemp -d /dev/shm/cnpg-minio.XXXXXX)
pf_pid=""
cleanup() {
  if [ -n "$pf_pid" ]; then kill "$pf_pid" 2>/dev/null || true; fi
  find "$work" -type f -exec shred -u {} + 2>/dev/null || true
  rm -rf "$work"
}
trap cleanup EXIT

# Vault first: a rotation must not change MinIO unless the new secret can be
# stored straight afterwards.
step "connecting to Vault"
kubectl -n vault port-forward svc/platform-vault 18230:8200 >/dev/null 2>&1 &
pf_pid=$!
export VAULT_ADDR=http://127.0.0.1:18230
code=000
for _ in $(seq 1 30); do
  code=$(curl -sS -o /dev/null -w '%{http_code}' "$VAULT_ADDR/v1/sys/health?standbyok=true" 2>/dev/null || true)
  [ "$code" = 200 ] && break
  sleep 1
done
[ "$code" = 200 ] || die "Vault not healthy through the port-forward (last HTTP status $code)"
VAULT_TOKEN=$(kubectl -n vault get secret vault-init -o go-template='{{index .data "vault-init.json"}}' \
  | base64 -d | python3 -c 'import json,sys; print(json.load(sys.stdin)["root_token"])')
export VAULT_TOKEN
[ -n "$VAULT_TOKEN" ] || die "no root token in vault/vault-init"
vault token lookup >/dev/null 2>&1 || die "the vault-init root token is not accepted"

# Captured rather than piped into grep -q: under pipefail an early-exiting grep
# can fail the pipeline with SIGPIPE.
mc_version=$(mc --version 2>/dev/null || true)
if [[ ${mc_version,,} == *"mc version"* ]]; then
  MC=$(command -v mc)
else
  MC=$MC_CACHE/mc
  if [ ! -x "$MC" ]; then
    step "building mc ($MC_MODULE) into $MC_CACHE"
    mkdir -p "$MC_CACHE"
    GOBIN=$MC_CACHE go install "$MC_MODULE"
  fi
fi

mc_config() {
  export MC_CONFIG_DIR=$work/$1
  mkdir -p "$MC_CONFIG_DIR/certs/CAs"
  cp "$CA_FILE" "$MC_CONFIG_DIR/certs/CAs/minio-ca.crt"
}

mc_config mc-admin
step "MinIO admin login (mc prompts; the secret input is hidden)"
# No stdout redirect: mc writes its prompts there.
"$MC" alias set admin "$MINIO_URL" || die "admin login failed"
"$MC" admin info admin >/dev/null || die "those credentials do not have admin rights"

# Every bucket exists before any user is tested, so each deny check below has
# a real bucket to be denied.
for e in "${ALL_ENVS[@]}"; do
  "$MC" mb --ignore-existing "admin/cnpg-$e" >/dev/null
done

for e in "${envs[@]}"; do
  bucket=cnpg-$e user=cnpg-$e policy=cnpg-$e path=cnpg-$e-s3
  export MC_CONFIG_DIR=$work/mc-admin
  step "[$e] bucket $bucket, policy $policy"
  cat > "$work/policy.json" <<JSON
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": ["s3:GetBucketLocation", "s3:ListBucket", "s3:ListBucketMultipartUploads"],
      "Resource": ["arn:aws:s3:::$bucket"]
    },
    {
      "Effect": "Allow",
      "Action": ["s3:GetObject", "s3:PutObject", "s3:DeleteObject",
                 "s3:AbortMultipartUpload", "s3:ListMultipartUploadParts"],
      "Resource": ["arn:aws:s3:::$bucket/*"]
    }
  ]
}
JSON
  "$MC" admin policy create admin "$policy" "$work/policy.json" >/dev/null

  # Only a definite "no such user" counts as absent; any other error stops
  # here, so a network blip cannot turn into an unrequested rotation.
  if info=$("$MC" admin user info admin "$user" --json 2>&1); then
    [ "${ROTATE:-}" = 1 ] || die "[$e] user $user already exists; re-run with ROTATE=1 to issue a new secret"
    step "[$e] user $user exists: rotating its secret"
  elif [[ ${info,,} =~ nosuchuser|does\ not\ exist|not\ found ]]; then
    step "[$e] user $user"
  else
    die "[$e] could not tell whether user $user exists"
  fi

  gen=$(openssl rand -base64 60 | tr -dc 'A-Za-z0-9')
  printf '%s' "${gen:0:40}" > "$work/secret"
  unset gen
  [ "$(wc -c < "$work/secret")" -eq 40 ] || die "secret generation failed"
  printf '%s' "$user" > "$work/user"

  # Access key as an argument (it is the user name); the secret on stdin, which
  # mc reads when stdin is not a terminal, so it never reaches argv.
  "$MC" admin user add admin "$user" < "$work/secret" >/dev/null
  vault kv put -mount="$VAULT_MOUNT" "$path" \
    ACCESS_KEY_ID=@"$work/user" ACCESS_SECRET_KEY=@"$work/secret" >/dev/null \
    || die "[$e] MinIO has the new secret but Vault does not: re-run with ROTATE=1"
  want=$(sha256sum < "$work/secret" | cut -d' ' -f1)
  got=$(vault kv get -mount="$VAULT_MOUNT" -field=ACCESS_SECRET_KEY "$path" | tr -d '\n' | sha256sum | cut -d' ' -f1)
  [ "$want" = "$got" ] || die "[$e] Vault value does not match the MinIO secret"
  echo "    Vault $VAULT_MOUNT/$path written (sha256 compared, not printed)"

  # Attach unconditionally: the entities listing names the user too, and the
  # user and policy share a name, so searching it for the policy proves nothing.
  if ! out=$("$MC" admin policy attach admin "$policy" --user "$user" 2>&1); then
    [[ ${out,,} == *"already"* ]] || die "[$e] could not attach policy $policy to $user"
  fi

  es=postgresql-cluster-$e-barman-s3
  if kubectl -n database get externalsecret "$es" >/dev/null 2>&1; then
    kubectl -n database annotate externalsecret "$es" force-sync="$(date +%s)" --overwrite >/dev/null
    echo "    ExternalSecret $es asked to refresh"
  fi

  step "[$e] checking $user's access"
  mc_config "mc-$e"
  { echo "$user"; cat "$work/secret"; echo; } | "$MC" alias set user "$MINIO_URL" >/dev/null
  echo "probe $(date -u +%FT%TZ)" > "$work/probe"
  "$MC" cp -q "$work/probe" "user/$bucket/.setup-probe" >/dev/null || die "[$e] $user cannot write to $bucket"
  "$MC" ls "user/$bucket/.setup-probe" >/dev/null || die "[$e] $user cannot list $bucket"
  "$MC" rm -q "user/$bucket/.setup-probe" >/dev/null || die "[$e] $user cannot delete in $bucket"
  for other in "${ALL_ENVS[@]}"; do
    [ "$other" = "$e" ] && continue
    if "$MC" ls "user/cnpg-$other" >/dev/null 2>&1; then
      die "[$e] $user can list cnpg-$other: its policy is wider than $bucket"
    fi
  done
  echo "    write/list/delete in $bucket: ok; other cnpg buckets: denied"
done

echo
echo "Done: ${envs[*]}. The clusters pick the keys up through their ExternalSecrets."
