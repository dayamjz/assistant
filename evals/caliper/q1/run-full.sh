#!/bin/sh
# run-full.sh [MODEL]
#
# Full Q1 both arms (issue #287 items 4–5): k = 5, the same pinned
# backend:model for --model and --judge-model, with and without the
# assistant skill. MODEL defaults to claude-code:claude-sonnet-4-5
# (pin explicitly; do not leave the CLI default).
#
# Prerequisites: `make build` at repo root, caliper-eval 0.19.0 on PATH,
# Claude Code logged in (ANTHROPIC_API_KEY or `claude auth login`), and
# provider headroom checked (quota-axi). Graders must pass first.
set -eu
cd "$(dirname "$0")"
MODEL="${1:-claude-code:claude-sonnet-4-5}"
REPO_ROOT="$(CDPATH= cd -- ../../.. && pwd)"
export PATH="$REPO_ROOT/bin:${HOME}/.local/bin:${HOME}/.local/node_modules/.bin:${PATH}"

if ! command -v assistant >/dev/null 2>&1; then
	echo "assistant not on PATH; run make build at the repo root" >&2
	exit 2
fi
if ! command -v caliper >/dev/null 2>&1; then
	echo "caliper not on PATH; pip install --user 'caliper-eval==0.19.0'" >&2
	exit 2
fi
if ! command -v claude >/dev/null 2>&1; then
	echo "claude not on PATH; install Claude Code for the pilot backend" >&2
	exit 2
fi
# Caliper isolates HOME per attempt, so a logged-in interactive shell is
# not enough: refuse before graders when this process cannot authenticate.
if ! claude auth status 2>/dev/null | grep -q '"loggedIn": true'; then
	if [ -z "${ANTHROPIC_API_KEY:-}" ]; then
		echo "Claude Code is not logged in and ANTHROPIC_API_KEY is unset; run claude auth login or export the key" >&2
		exit 2
	fi
fi

python3 verify_graders.py
caliper validate q1.eval.yaml

# workers=1: build-subject.sh installs a global insteadOf for the subject
# upstream URL so the gate bare repo rewrites too; two attempts sharing this
# machine's HOME would race that rewrite. Caliper's attempt HOME isolation
# does not cover the setup hook's git config --global here.
echo "running with-skill arm (model=$MODEL)" >&2
caliper run q1.eval.yaml --k 5 --timeout 1200 --workers 1 \
	--model "$MODEL" --judge-model "$MODEL" \
	--output result-q1-with-skill.json

echo "running ablated arm (model=$MODEL)" >&2
caliper run q1.eval.yaml --k 5 --timeout 1200 --workers 1 \
	--model "$MODEL" --judge-model "$MODEL" \
	--ablate assistant --output result-q1-ablated.json

caliper compare result-q1-with-skill.json result-q1-ablated.json
echo "results: result-q1-with-skill.json result-q1-ablated.json" >&2
echo "collect gate usage from \${TMPDIR}/caliper-q1-gate/ into the write-up" >&2
