#!/bin/sh
# run-smoke.sh [MODEL]
#
# Activation smoke (issue #895): k = 1 on defect + benign with activates
# expectations. After the run, score_result.py fails any usable attempt whose
# transcript never invoked the gate (#398).
set -eu
cd "$(dirname "$0")/../.."
MODEL="${1:-claude-code:claude-sonnet-4-5}"
REPO_ROOT="$(CDPATH= cd -- ../../.. && pwd)"
export PATH="$REPO_ROOT/bin:${HOME}/.local/bin:${HOME}/.local/node_modules/.bin:${PATH}"
OUT=instruments/activation_smoke/result-smoke.json

if ! command -v assistant >/dev/null 2>&1; then
	echo "assistant not on PATH; run make build at the repo root" >&2
	exit 2
fi
if ! command -v caliper >/dev/null 2>&1; then
	echo "caliper not on PATH; pip install --user 'caliper-eval==0.19.0'" >&2
	exit 2
fi
if ! command -v claude >/dev/null 2>&1; then
	echo "claude not on PATH; install Claude Code for the smoke backend" >&2
	exit 2
fi
if ! claude auth status 2>/dev/null | grep -q '"loggedIn": true'; then
	if [ -z "${ANTHROPIC_API_KEY:-}" ]; then
		echo "Claude Code is not logged in and ANTHROPIC_API_KEY is unset" >&2
		exit 2
	fi
fi

python3 instruments/activation_smoke/verify_smoke.py
caliper validate instruments/activation_smoke/smoke.eval.yaml

echo "cost: activation smoke = 2 attempts (defect + benign, k=1), model=$MODEL" >&2
echo "cost: estimate ~0.3M-0.7M agent tokens at Sonnet list rates; confirm with quota-axi before a larger run" >&2

git config --global --get-regexp '^url\..*\.insteadof$' \
	'https://github.com/dayamjz/caliper-q1.git' 2>/dev/null |
	while read -r key _; do
		git config --global --unset-all "$key" 2>/dev/null || true
	done

caliper run instruments/activation_smoke/smoke.eval.yaml \
	--k 1 --timeout 1200 --workers 1 \
	--model "$MODEL" --judge-model "$MODEL" \
	--output "$OUT"

python3 instruments/activation_smoke/score_result.py "$OUT"
echo "results: $OUT" >&2
