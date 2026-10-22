#!/bin/sh
# Shared cleanup for every Q1 task: drop every global insteadOf rewrite of the
# subject upstream URL (so a failed setup that never created work/ cannot leave
# a dead path for the next attempt), save the gate's usage from the attempt's
# assistant home, then stop that service.
set -eu
UPSTREAM_URL="https://github.com/dayamjz/caliper-q1.git"
# Clear by URL, not by this attempt's remote path: cleanup used to require
# ../remote.git, and a setup that failed after installing insteadOf then
# exited here with the rewrite still pointing at a deleted tree. Attempt 5 of
# the 2026-10-16 full with-skill arm failed that way on every task.
git config --global --get-regexp '^url\..*\.insteadof$' "$UPSTREAM_URL" 2>/dev/null |
	while read -r key _; do
		git config --global --unset-all "$key" 2>/dev/null || true
	done

# Caliper runs this hook with cwd=$CALIPER_WORKDIR (<attempt>/work). The agent
# starts the service under <attempt>/.assistant. build-subject also creates a
# nested ./work here, so testing for [ -d work ] and cd'ing into it both
# mis-keyed the gate dump and left assistant talking to the operator HOME
# (every 2026-10-16 full-run capture failed that way).
workdir="${CALIPER_WORKDIR:-.}"
attempt_root="$(CDPATH= cd -- "$workdir/.." && pwd)"
home="$attempt_root/.assistant"
out="${TMPDIR:-/tmp}/caliper-q1-gate/$(basename "$attempt_root")"
mkdir -p "${TMPDIR:-/tmp}/caliper-q1-gate"
if [ -d "$home" ]; then
	assistant --home "$home" --json runs --usage=1h > "$out-usage.json" 2>/dev/null || true
	assistant --home "$home" --json runs > "$out-runs.json" 2>/dev/null || true
	assistant --home "$home" service stop --force >/dev/null 2>&1 || true
fi
