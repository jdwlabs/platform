# Agent tooling traps

Symptoms in this repo and the cluster it deploys that look like success, or
like a repo problem, but come from the tooling in front of them. Pointed to
from the root `AGENTS.md`.

Box-wide tool traps: `~/.local/share/chezmoi/docs/agent-tooling-traps.md` (dotfiles).

| Symptom | Cause | Fix |
|---|---|---|
| `.status.containerStatuses[].image` disagrees with the pod spec — a bare `sha256:…` with no repo, or a digest that matches nothing you deployed | That field carries the **config** digest, reported under whichever reference resolved first; `.imageID` carries the repo plus the **manifest** digest. Sampled live: `.image` was `sha256:9700374b…` with no repo while `.imageID` was `docker.io/jdwlabs/ai-sre-relay@sha256:f42b749b…` — two different digests for one running container | Read `.imageID`, never `.status…image`, when verifying which image is running. If the repo names still disagree, compare config/layer digests rather than concluding the wrong image is deployed |
| A PR that was `mergeable` goes `dirty`/`BLOCKED` with zero CI runs registered for the latest push, sometimes for many minutes | `pull_request`-triggered workflows check out the `refs/pull/<n>/merge` ref, and GitHub can't materialize that ref once the branch conflicts with the current `main` tip — so no run is ever created, independent of merge strategy. This repo's `required_linear_history` only decides *which* merge method can land (rebase here — `allow_squash_merge`/`allow_merge_commit` are both off), not whether checks queue | `gh api repos/<owner>/<repo>/pulls/<n> --jq '{mergeable, mergeable_state}'` to confirm before assuming CI is stuck; if `dirty`, `git fetch origin main && git rebase origin/main`, resolve, push, checks register within seconds |
| A locally-resolved conflict reappears at merge time even though the branch showed no conflict before pushing | Resolving with `git merge origin/main` creates a merge commit — but GitHub's rebase-merge button replays each of the branch's **original** commits individually and silently discards merge commits, so the pre-resolution conflict comes back exactly as if nothing was fixed | On a rebase-only repo, always resolve with `git rebase origin/main` (never `git merge origin/main`), then `git push --force-with-lease` — this replays your commits on the new base and the resolution actually sticks. This repo also requires the `signatures` check, and a plain `git rebase` only re-signs replayed commits if `commit.gpgsign=true` (or pass `-S` explicitly) |
