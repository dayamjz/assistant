#!/bin/sh
# run-full.sh [MODEL]
#
# Full Q1 (issue #287 items 4–5, row 11 a#896): k = 5 for each arm in README
# "Arms", the same pinned backend:model for --model and --judge-model. MODEL
# defaults to claude-code:claude-sonnet-5-5, the owner's pin for row 11; do
# not leave the CLI default. Q1_K overrides k.
#
# Prerequisites: `make build` at repo root, install-caliper.sh's caliper
# (in CALIPER_VENV, the same default as there),
# Claude Code logged in (ANTHROPIC_API_KEY or `claude auth login`), and
# provider headroom checked (quota-axi). Graders must pass first.
set -eu
MODEL="${1:-claude-code:claude-sonnet-5-5}"
UPSTREAM_URL="https://github.com/dayamjz/caliper-q1.git"

# A run never writes the operator's global git config (a#942): where that is a
# read-only link, every setup hook failed. Before anything reads or writes git
# config, GIT_CONFIG_GLOBAL names a scratch file of this run's, which includes
# the operator's own global config for reading. Caliper runs setup, cleanup and
# assert with this process's environment (caliper/workdir.py), so their global
# writes land in the scratch file too; the agent gets an environment of its own
# (README "Subject remote"). The file is removed on exit, and an interrupt
# exits through that same trap.
trap '[ -z "${GITCONFIG:-}" ] || rm -f "$GITCONFIG"' EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM
GITCONFIG="$(mktemp "${TMPDIR:-/tmp}/q1-gitconfig.XXXXXX")"
if [ -n "${GIT_CONFIG_GLOBAL:-}" ]; then
	set -- "$GIT_CONFIG_GLOBAL"
else
	set -- "${XDG_CONFIG_HOME:-$HOME/.config}/git/config" "$HOME/.gitconfig"
fi
for real; do
	case "$real" in /*) ;; *) real="$PWD/$real" ;; esac
	# This git reads no global config: the refusal below reports one it cannot read.
	[ ! -e "$real" ] || GIT_CONFIG_GLOBAL=/dev/null git config --file "$GITCONFIG" --add include.path "$real"
done
export GIT_CONFIG_GLOBAL="$GITCONFIG"
# A rewrite of the subject URL in the operator's own config, which a run
# before a#942 could leave there, would win over each attempt's own rewrite.
# Clearing it would write that config, so refuse instead and name it.
stale_rc=0
stale="$(git config --global --includes --show-origin --get-regexp '^url\..*\.insteadof$' "$UPSTREAM_URL")" || stale_rc=$?
case "$stale_rc" in
0)
	echo "your global git config rewrites $UPSTREAM_URL, which would win over every attempt's rewrite; remove it and rerun:" >&2
	printf '%s\n' "$stale" >&2
	exit 2
	;;
1) ;;
*)
	echo "cannot read your global git config through $GITCONFIG (git config exit $stale_rc)" >&2
	exit 2
	;;
esac

cd "$(dirname "$0")"
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
# upstream URL for the hook's own git, in this run's one scratch file, so two
# concurrent attempts would race that rewrite. Caliper's attempt HOME isolation
# does not cover the setup hook's git config --global (README "Subject remote").

# Each arm runs under a TMPDIR of its own, so caliper makes that arm's attempt
# roots there and cleanup.sh writes their gate records to
# $ARMS/<arm>/caliper-q1-gate, never beside the other arm's
# (README "Gate verdicts by arm"). Each caliper run gets Q1_ARM, the arm its
# hooks record (README "Arms").
# An attempt's service listens on <attempt root>/.assistant/socket, so the
# arms go under Q1_ARMS_ROOT (default /tmp), never $TMPDIR: under macOS's
# $TMPDIR that path was too long to bind and no with-skill service ever ran
# (#941). Before any attempt, a service is started at a home as deep as the
# deepest attempt's (caliper's mkdtemp caliper-XXXXXXXX in the longer arm
# name); one that does not answer refuses the run and is kept for reading.
ARMS="$(mktemp -d "${Q1_ARMS_ROOT:-/tmp}/q1-arms.XXXXXX")"
echo "arms (each arm's TMPDIR): $ARMS" >&2
mkdir -p "$ARMS/full-gate/caliper-q1-gate" "$ARMS/agent-ci/caliper-q1-gate"
probe_root="$(mktemp -d "$ARMS/full-gate/caliper-XXXXXXXX")"
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
rm -f result-q1-full-gate.json result-q1-agent-ci.json

with_rc=0
echo "running full-gate arm (model=$MODEL)" >&2
Q1_ARM=full-gate TMPDIR="$ARMS/full-gate" caliper run q1.eval.yaml --k "$K" --timeout 1200 --workers 1 \
	--model "$MODEL" --judge-model "$MODEL" \
	--output result-q1-full-gate.json || with_rc=$?

# Clear the scratch file's rewrite of the subject URL before the agent-ci arm:
# cleanup should have done this, but a mid-arm crash can leave it pointing at a
# deleted remote.git, and must not poison the second arm.
git config --global --get-regexp '^url\..*\.insteadof$' "$UPSTREAM_URL" 2>/dev/null |
	while read -r key _; do
		git config --global --unset-all "$key" 2>/dev/null || true
	done

ablated_rc=0
echo "running agent-ci arm (model=$MODEL)" >&2
Q1_ARM=agent-ci TMPDIR="$ARMS/agent-ci" caliper run q1.eval.yaml --k "$K" --timeout 1200 --workers 1 \
	--model "$MODEL" --judge-model "$MODEL" \
	--ablate assistant --output result-q1-agent-ci.json || ablated_rc=$?

report_rc=0
if [ -f result-q1-full-gate.json ] && [ -f result-q1-agent-ci.json ]; then
	caliper compare result-q1-full-gate.json result-q1-agent-ci.json || true
	vcs() { go version -m "$REPO_ROOT/bin/assistant" | sed -n "s/^[[:space:]]*build[[:space:]]*vcs.$1=//p"; }
	python3 gate_by_arms.py --revision "$(vcs revision)" --modified "$(vcs modified)" \
		--arm full-gate "$ARMS/full-gate" result-q1-full-gate.json \
		--arm agent-ci "$ARMS/agent-ci" result-q1-agent-ci.json >"$ARMS/gate-by-arm.json" || report_rc=$?
else
	echo "no gate verdicts by arm: an arm wrote no result file" >&2
	report_rc=1
fi
echo "results: result-q1-full-gate.json result-q1-agent-ci.json" >&2
echo "gate records and usage per arm: $ARMS/<arm>/caliper-q1-gate/; gate verdicts by arm: $ARMS/gate-by-arm.json" >&2
if [ "$with_rc" -ne 0 ] || [ "$ablated_rc" -ne 0 ] || [ "$report_rc" -ne 0 ]; then
	echo "exit status: full-gate=$with_rc agent-ci=$ablated_rc gate-by-arm=$report_rc" >&2
	exit 1
fi

