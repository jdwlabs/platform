#!/usr/bin/env bash
#
# Opens (or idempotently re-targets) a pull request authored by the
# jdwlabs-agent-bot GitHub App identity, so agent-authored commits never trip
# agent-identity/co-author-check (docs/adr/0018-agentic-app-topology.md §3).
#
# Local-tooling counterpart to §4's workflow-side create-github-app-token
# step: there is no CI workflow that opens agent-authored PRs in any of the
# four repos today — every one of them is opened by a local `gh pr create`
# call from a Claude Code session. This script is the identity switch for
# that call: mint a short-lived installation token from the App credentials
# already wired by setup-agentic-app-identity.sh, and use it in place of the
# operator's own gh auth for exactly the one API call that needs to be the
# bot.
#
# Requires scripts/setup-agentic-app-identity.sh to have been run once
# (reads its ~/.config/jdwlabs/agent-app-identity.env output). Requires the
# operator's own `gh` session for the org-admin installation lookup and for
# closing a stale human-authored PR — only the final `gh pr create` runs
# under the minted bot token.

set -euo pipefail

BIN="$0"
ORG="jdwlabs"
ENV_FILE="${ENV_FILE:-$HOME/.config/jdwlabs/agent-app-identity.env}"

usage() {
  cat <<EOF
bin: ${BIN}
description: Open a pull request authored by jdwlabs-agent-bot, closing a stale human-authored PR on the same head first.

usage: $(basename "$BIN") --repo <owner/repo> --head <branch> --title <title> --body-file <path> [--base <branch>]

flags:
  --repo <owner/repo>   required. Target repository, e.g. jdwlabs/platform.
  --head <branch>       required. Branch to open the PR from. Must already exist on the remote.
  --title <title>       required. PR title.
  --body-file <path>    required. Path to a file containing the PR body (markdown).
  --base <branch>       optional. Defaults to main.
  --help                show this help and exit 0.

examples:
  $(basename "$BIN") --repo jdwlabs/platform --head fix/JDWLABS-123-thing --title "fix(x): thing" --body-file /tmp/body.md
  $(basename "$BIN") --repo jdwlabs/apps --head feat/JDWLABS-456-y --base main --title "feat(y): y" --body-file body.md
EOF
}

err() {
  printf 'error: %s\n' "$1"
  [ -n "${2:-}" ] && printf 'help: %s\n' "$2"
  exit "${3:-1}"
}

REPO=""
HEAD=""
BASE="main"
TITLE=""
BODY_FILE=""

while [ $# -gt 0 ]; do
  case "$1" in
    --repo) REPO="${2:-}"; shift 2 ;;
    --head) HEAD="${2:-}"; shift 2 ;;
    --base) BASE="${2:-}"; shift 2 ;;
    --title) TITLE="${2:-}"; shift 2 ;;
    --body-file) BODY_FILE="${2:-}"; shift 2 ;;
    --help) usage; exit 0 ;;
    *) err "unknown flag $1" "$(basename "$BIN") --help lists valid flags" 2 ;;
  esac
done

[ -n "$REPO" ] || err "--repo is required" "$(basename "$BIN") --repo <owner/repo> --head <branch> --title <title> --body-file <path>" 2
[ -n "$HEAD" ] || err "--head is required" "$(basename "$BIN") --repo <owner/repo> --head <branch> --title <title> --body-file <path>" 2
[ -n "$TITLE" ] || err "--title is required" "$(basename "$BIN") --repo <owner/repo> --head <branch> --title <title> --body-file <path>" 2
[ -n "$BODY_FILE" ] || err "--body-file is required" "$(basename "$BIN") --repo <owner/repo> --head <branch> --title <title> --body-file <path>" 2
[ -f "$BODY_FILE" ] || err "--body-file '${BODY_FILE}' does not exist" "" 1

if [ ! -f "$ENV_FILE" ]; then
  err "no agent App identity found at ${ENV_FILE}" "run scripts/setup-agentic-app-identity.sh first" 1
fi

AGENT_APP_ID=$(grep -E '^AGENT_APP_ID=' "$ENV_FILE" | tail -n1 | cut -d= -f2-)
AGENT_PRIVATE_KEY_PATH=$(grep -E '^AGENT_PRIVATE_KEY_PATH=' "$ENV_FILE" | tail -n1 | cut -d= -f2-)

[ -n "$AGENT_APP_ID" ] || err "AGENT_APP_ID missing from ${ENV_FILE}" "re-run scripts/setup-agentic-app-identity.sh" 1
if [ -z "$AGENT_PRIVATE_KEY_PATH" ] || [ ! -f "$AGENT_PRIVATE_KEY_PATH" ]; then
  err "private key not found at ${AGENT_PRIVATE_KEY_PATH:-<unset>}" "re-run scripts/setup-agentic-app-identity.sh" 1
fi

if ! command -v gh >/dev/null 2>&1 || ! gh auth status >/dev/null 2>&1; then
  err "gh is not authenticated" "run 'gh auth login' as an org-admin account" 1
fi

# Step 1: idempotent — a PR already open under the bot identity on this head
# needs nothing further. One already open under a human identity is closed
# first so gh pr create doesn't collide with it.
existing=$(gh pr list --repo "$REPO" --head "$HEAD" --state open --json number,author 2>/dev/null || echo '[]')
existing_count=$(printf '%s' "$existing" | jq 'length')

if [ "$existing_count" -gt 0 ]; then
  existing_number=$(printf '%s' "$existing" | jq -r '.[0].number')
  existing_login=$(printf '%s' "$existing" | jq -r '.[0].author.login')
  case "$existing_login" in
    jdwlabs-agent-bot|jdwlabs-agent-bot\[bot\])
      pr_url=$(gh pr view "$existing_number" --repo "$REPO" --json url -q .url)
      printf 'pr: number=%s url=%s author=%s state=already-open\n' "$existing_number" "$pr_url" "$existing_login"
      exit 0
      ;;
    *)
      gh pr close "$existing_number" --repo "$REPO" --comment \
        "Closing to reopen under jdwlabs-agent-bot — blocked by agent-identity/co-author-check (jdwlabs/platform docs/adr/0018-agentic-app-topology.md §3). Same branch, same commits, no code change." \
        >/dev/null
      ;;
  esac
fi

# Step 2: resolve the installation id for this org. Requires org-admin read
# on the operator's own gh session — the bot token can't bootstrap itself.
installation_id=$(gh api "orgs/${ORG}/installations" --jq \
  ".installations[] | select(.app_id == ${AGENT_APP_ID}) | .id" 2>/dev/null || true)
[ -n "$installation_id" ] || err "jdwlabs-agent-bot (app_id ${AGENT_APP_ID}) is not installed on org ${ORG}" \
  "check https://github.com/organizations/${ORG}/settings/installations" 1

# Step 3: mint a short-lived installation token (JWT signed with the App's
# private key, exchanged for an installation access token). Written to a
# 600-permission temp file rather than a variable that could leak into a
# trap/error trace; removed unconditionally on exit.
token_file=$(mktemp)
chmod 600 "$token_file"
trap 'rm -f "$token_file"' EXIT

python3 - "$AGENT_APP_ID" "$AGENT_PRIVATE_KEY_PATH" "$installation_id" "$token_file" <<'PYEOF'
import sys, time, jwt, requests

app_id, key_path, installation_id, out_path = sys.argv[1:5]
with open(key_path) as f:
    private_key = f.read()

now = int(time.time())
encoded_jwt = jwt.encode(
    {"iat": now - 60, "exp": now + 600, "iss": app_id},
    private_key,
    algorithm="RS256",
)

resp = requests.post(
    f"https://api.github.com/app/installations/{installation_id}/access_tokens",
    headers={
        "Authorization": f"Bearer {encoded_jwt}",
        "Accept": "application/vnd.github+json",
        "X-GitHub-Api-Version": "2022-11-28",
    },
    timeout=30,
)
resp.raise_for_status()
with open(out_path, "w") as f:
    f.write(resp.json()["token"])
PYEOF

# Step 4: open the PR as the bot. GH_TOKEN scoped to this subshell only.
pr_url=$(GH_TOKEN=$(cat "$token_file") gh pr create --repo "$REPO" --head "$HEAD" --base "$BASE" \
  --title "$TITLE" --body-file "$BODY_FILE")
pr_number="${pr_url##*/}"

printf 'pr: number=%s url=%s author=jdwlabs-agent-bot state=opened\n' "$pr_number" "$pr_url"
