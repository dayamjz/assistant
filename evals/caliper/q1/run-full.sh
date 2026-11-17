#!/bin/sh
# run-full.sh [MODEL]
#
# Full Q1 both arms (issue #287 items 4–5): k = 5, the same pinned
# backend:model for --model and --judge-model, with and without the
# assistant skill. MODEL defaults to claude-code:claude-sonnet-4-5
# (pin explicitly; do not leave the CLI default). Q1_K overrides k.
#
# Prerequisites: `make build` at repo root, install-caliper.sh's caliper
# (in CALIPER_VENV, the same default as there),
# Claude Code logged in (ANTHROPIC_API_KEY or `claude auth login`), and
# provider headroom checked (quota-axi). Graders must pass first.
set -eu
cd "$(dirname "$0")"
MODEL="${1:-claude-code:claude-sonnet-4-5}"
REPO_ROOT="$(CDPATH= cd -- ../../.. && pwd)"
CALIPER_VENV="${CALIPER_VENV:-$HOME/.local/share/caliper-q1attemptenv}"
export PATH="$REPO_ROOT/bin:$CALIPER_VENV/bin:${HOME}/.local/bin:${HOME}/.local/node_modules/.bin:${PATH}"
K="${Q1_K:-5}"

if ! command -v assistant >/dev/null 2>&1; then
	echo "assistant not on PATH; run make build at the repo root" >&2
	exit 2
fi
# Only the patched caliper passes cleanup.sh the attempt key "as run" joins on
# (README "As run"). Ask the caliper on PATH through the interpreter it runs on.
caliper="$(command -v caliper || true)"
version="$([ -n "$caliper" ] && "$(sed -n '1s/^#!//p' "$caliper")" -c \
	'import importlib.metadata as m; print(m.version("caliper-eval"))' 2>/dev/null || true)"
if [ "$version" != 0.19.0+q1attemptenv ]; then
	echo "caliper on PATH ('$caliper') is caliper-eval '$version', not 0.19.0+q1attemptenv; run ./install-caliper.sh" >&2
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
# upstream URL for the hook's own git; two attempts sharing this machine's
# HOME would race that rewrite. Caliper's attempt HOME isolation does not
# cover the setup hook's git config --global here (README "Subject remote").
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
# An attempt's service listens on <attempt root>/.assistant/socket, so the
# arms go under Q1_ARMS_ROOT (default /tmp), never $TMPDIR: under macOS's
# $TMPDIR that path was too long to bind and no with-skill service ever ran
# (#941). Before any attempt, a service is started at a home as deep as the
# deepest attempt's (caliper's mkdtemp caliper-XXXXXXXX in the longer arm
# name); one that does not answer refuses the run and is kept for reading.
ARMS="$(mktemp -d "${Q1_ARMS_ROOT:-/tmp}/q1-arms.XXXXXX")"
echo "arms (each arm's TMPDIR): $ARMS" >&2
mkdir -p "$ARMS/with-skill/caliper-q1-gate" "$ARMS/ablated/caliper-q1-gate"
probe_root="$(mktemp -d "$ARMS/with-skill/caliper-XXXXXXXX")"
probe="$probe_root/.assistant"
if ! assistant --home "$probe" service start >"$ARMS/probe.log" 2>&1; then
	echo "a service at $probe, an attempt's depth under $ARMS, did not start; set Q1_ARMS_ROOT to a shorter directory (README \"How to run\"):" >&2
	cat "$ARMS/probe.log" "$probe/logs/service.log" >&2 || true
	exit 2
fi
if ! assistant --home "$probe" service stop --force >>"$ARMS/probe.log" 2>&1; then
	echo "the probe service at $probe did not accept its stop; see $ARMS/probe.log" >&2
	exit 2
fi
# The stop answers once accepted, so the probe home is removed only once status
# says the service is down; any other answer waits, then refuses.
i=0
until status="$(assistant --home "$probe" --json service status 2>&1)" &&
	case "$status" in *'"running":false'*) true ;; *) false ;; esac; do
	i=$((i + 1))
	if [ "$i" -ge 30 ]; then
		echo "the probe service at $probe is not reported down 30s after its stop: $status" >&2
		exit 2
	fi
	sleep 1
done
rm -rf "${probe_root:?}"
# A result file left from an earlier run must never be read as this run's.
rm -f result-q1-with-skill.json result-q1-ablated.json

with_rc=0
echo "running with-skill arm (model=$MODEL)" >&2
TMPDIR="$ARMS/with-skill" caliper run q1.eval.yaml --k "$K" --timeout 1200 --workers 1 \
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
TMPDIR="$ARMS/ablated" caliper run q1.eval.yaml --k "$K" --timeout 1200 --workers 1 \
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

