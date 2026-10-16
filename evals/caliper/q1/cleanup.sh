#!/bin/sh
# Shared cleanup for every Q1 task: save the gate's usage and runs under a
# name keyed on the caliper attempt directory, then stop the service.
set -eu
mkdir -p "${TMPDIR:-/tmp}/caliper-q1-gate"
cd work 2>/dev/null || exit 0
out="${TMPDIR:-/tmp}/caliper-q1-gate/$(basename "$(cd ../.. && pwd)")"
assistant --json runs --usage=1h > "$out-usage.json" 2>/dev/null || true
assistant --json runs > "$out-runs.json" 2>/dev/null || true
assistant service stop --force >/dev/null 2>&1 || true
