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
if ! command -v go >/dev/null 2>&1; then
	echo "go not on PATH; gate_by_arms.py needs the built binary's vcs stamp" >&2
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
# Clear any leftover rewrite of the subject URL before either arm. A prior
# crashed run can leave url.*.insteadOf pointing at a deleted remote.git.
git config --global --get-regexp '^url\..*\.insteadof$' \
	'https://github.com/dayamjz/caliper-q1.git' 2>/dev/null |
	while read -r key _; do
		git config --global --unset-all "$key" 2>/dev/null || true
	done

# Each arm runs under a TMPDIR of its own, so caliper makes that arm's attempt
# roots there and cleanup.sh writes their gate records to
# $ARMS/<arm>/caliper-q1-gate, never beside the other arm's
# (README "Gate verdicts by arm").
ARMS="$(mktemp -d "${TMPDIR:-/tmp}/caliper-q1-arms.XXXXXX")"
mkdir -p "$ARMS/with-skill/caliper-q1-gate" "$ARMS/ablated/caliper-q1-gate"
# A result file left from an earlier run must never be read as this run's.
rm -f result-q1-with-skill.json result-q1-ablated.json

with_rc=0
echo "running with-skill arm (model=$MODEL)" >&2
TMPDIR="$ARMS/with-skill" caliper run q1.eval.yaml --k 5 --timeout 1200 --workers 1 \
	--model "$MODEL" --judge-model "$MODEL" \
	--output result-q1-with-skill.json || with_rc=$?

# Same URL clear before the ablated arm: cleanup should have done this, but a
# mid-arm crash must not poison the second arm.
git config --global --get-regexp '^url\..*\.insteadof$' \
	'https://github.com/dayamjz/caliper-q1.git' 2>/dev/null |
	while read -r key _; do
		git config --global --unset-all "$key" 2>/dev/null || true
	done

ablated_rc=0
echo "running ablated arm (model=$MODEL)" >&2
TMPDIR="$ARMS/ablated" caliper run q1.eval.yaml --k 5 --timeout 1200 --workers 1 \
	--model "$MODEL" --judge-model "$MODEL" \
	--ablate assistant --output result-q1-ablated.json || ablated_rc=$?

report_rc=0
if [ -f result-q1-with-skill.json ] && [ -f result-q1-ablated.json ]; then
	caliper compare result-q1-with-skill.json result-q1-ablated.json || true
	vcs() { go version -m "$REPO_ROOT/bin/assistant" | sed -n "s/^[[:space:]]*build[[:space:]]*vcs.$1=//p"; }
	python3 gate_by_arms.py --revision "$(vcs revision)" --modified "$(vcs modified)" \
		--arm with-skill "$ARMS/with-skill" result-q1-with-skill.json \
		--arm ablated "$ARMS/ablated" result-q1-ablated.json >"$ARMS/gate-by-arm.json" || report_rc=$?
else
	echo "no gate verdicts by arm: an arm wrote no result file" >&2
	report_rc=1
fi
echo "results: result-q1-with-skill.json result-q1-ablated.json" >&2
echo "gate records and usage per arm: $ARMS/<arm>/caliper-q1-gate/; gate verdicts by arm: $ARMS/gate-by-arm.json" >&2
if [ "$with_rc" -ne 0 ] || [ "$ablated_rc" -ne 0 ] || [ "$report_rc" -ne 0 ]; then
	echo "exit status: with-skill=$with_rc ablated=$ablated_rc gate-by-arm=$report_rc" >&2
	exit 1
fi

