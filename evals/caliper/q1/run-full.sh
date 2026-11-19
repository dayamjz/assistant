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
# exits through that same trap, which also ends a reviewer process (start_reviewer).
trap '[ -z "${REVIEWER_PID:-}" ] || stop_reviewer; [ -z "${GITCONFIG:-}" ] || rm -f "$GITCONFIG"' EXIT
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

# The arms of README "Arms" this script runs, in order. Every step below that
# is per arm reads this list, and the checks here run for each arm before
# graders: an arm arm_flags does not name is refused, never run as another.
ARM_LIST="full-gate agent-ci agent-ci-review"
# arm_flags ARM prints the caliper run flags that make ARM that arm.
arm_flags() {
	case "$1" in
	full-gate) ;;
	agent-ci | agent-ci-review) echo "--ablate assistant" ;;
	*) return 1 ;;
	esac
}
for arm in $ARM_LIST; do
	# agent-ci-review's reviewer runs claude -p under a fresh HOME, where only
	# this token can authenticate it; start_reviewer hands it to the reviewer
	# process, and caliper 0.19.0 passes it from this environment to the agent
	# (README "Arms"). The value is never printed.
	if [ "$arm" = agent-ci-review ] && [ -z "${CLAUDE_CODE_OAUTH_TOKEN:-}" ]; then
		echo "arm agent-ci-review needs CLAUDE_CODE_OAUTH_TOKEN, which is unset or empty; export it (claude setup-token) to run that arm" >&2
		exit 2
	fi
	if ! arm_flags "$arm" >/dev/null; then
		echo "run-full.sh names no caliper flags for arm '$arm' (arm_flags)" >&2
		exit 2
	fi
done

python3 verify_graders.py
caliper validate q1.eval.yaml

# workers=1: build-subject.sh installs a global insteadOf for the subject
# upstream URL for the hook's own git, in this run's one scratch file, so two
# concurrent attempts would race that rewrite. Caliper's attempt HOME isolation
# does not cover the setup hook's git config --global (README "Subject remote").
# clear_rewrites clears the scratch file's rewrite of the subject URL before
# each arm: a mid-arm crash can leave url.*.insteadOf pointing at a deleted
# remote.git, which must not poison the next arm. A rewrite in the operator's
# own config is refused above, never cleared.
clear_rewrites() {
	git config --global --get-regexp '^url\..*\.insteadof$' "$UPSTREAM_URL" 2>/dev/null |
		while read -r key _; do
			git config --global --unset-all "$key" 2>/dev/null || true
		done
}

# Each arm runs under a TMPDIR of its own, so caliper makes that arm's attempt
# roots there and cleanup.sh writes their gate records to
# $ARMS/<arm>/caliper-q1-gate, never beside another arm's
# (README "Gate verdicts by arm"). Each caliper run gets Q1_ARM, the arm its
# hooks record (README "Arms").
# An attempt's service listens on <attempt root>/.assistant/socket, so the
# arms go under Q1_ARMS_ROOT (default /tmp), never $TMPDIR: under macOS's
# $TMPDIR that path was too long to bind and no with-skill service ever ran
# (#941). Before any attempt, a service is started at a home as deep as the
# deepest attempt's (caliper's mkdtemp caliper-XXXXXXXX in the longest arm
# name); one that does not answer refuses the run and is kept for reading.
ARMS="$(mktemp -d "${Q1_ARMS_ROOT:-/tmp}/q1-arms.XXXXXX")"
echo "arms (each arm's TMPDIR): $ARMS" >&2
longest=
for arm in $ARM_LIST; do
	mkdir -p "$ARMS/$arm/caliper-q1-gate"
	if [ "${#arm}" -gt "${#longest}" ]; then longest="$arm"; fi
done
probe_root="$(mktemp -d "$ARMS/$longest/caliper-XXXXXXXX")"
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
results=
for arm in $ARM_LIST; do
	results="$results result-q1-$arm.json"
done
rm -f $results # one word per arm

# agent-ci-review's review hook runs inside the agent's git push, where
# Claude Code leaves CLAUDE_CODE_OAUTH_TOKEN out (a#968), so its reviewer runs
# in a process of this script's (README "Reviewer process"). start_reviewer
# starts review-server.py with the token on its standard input, from printf,
# a builtin, and with no token in its environment or argv, and waits until its
# socket, in a 0700 directory of its own under $ARMS, answers; setup puts
# Q1_REVIEWER_SOCKET in the hook. stop_reviewer ends it after the arm and on
# any exit, and removes that directory. One that does not start refuses the run.
REVIEWER_PID=
Q1_REVIEWER_SOCKET=
start_reviewer() {
	REVIEWER_DIR="$(mktemp -d "$ARMS/reviewer.XXXXXX")"
	printf '%s' "$CLAUDE_CODE_OAUTH_TOKEN" | env -u CLAUDE_CODE_OAUTH_TOKEN TMPDIR="$REVIEWER_DIR" \
		python3 review-server.py "$REVIEWER_DIR/socket" >"$ARMS/reviewer.log" 2>&1 &
	REVIEWER_PID=$!
	i=0
	until [ -S "$REVIEWER_DIR/socket" ]; do
		i=$((i + 1))
		if [ "$i" -gt 100 ] || ! kill -0 "$REVIEWER_PID" 2>/dev/null; then
			echo "the reviewer process at $REVIEWER_DIR/socket did not start; see $ARMS/reviewer.log" >&2
			exit 2
		fi
		sleep 0.1
	done
	Q1_REVIEWER_SOCKET="$REVIEWER_DIR/socket"
}
stop_reviewer() {
	kill "$REVIEWER_PID" 2>/dev/null || true
	wait "$REVIEWER_PID" 2>/dev/null || true
	rm -rf "${REVIEWER_DIR:?}"
	REVIEWER_PID= Q1_REVIEWER_SOCKET=
}

arm_status=
failed=0
for arm in $ARM_LIST; do
	clear_rewrites
	echo "running $arm arm (model=$MODEL)" >&2
	[ "$arm" != agent-ci-review ] || start_reviewer
	rc=0
	Q1_ARM="$arm" Q1_REVIEWER_SOCKET="$Q1_REVIEWER_SOCKET" TMPDIR="$ARMS/$arm" caliper run q1.eval.yaml \
		--k "$K" --timeout 1200 --workers 1 --model "$MODEL" --judge-model "$MODEL" \
		$(arm_flags "$arm") --output "result-q1-$arm.json" || rc=$?
	[ -z "$REVIEWER_PID" ] || stop_reviewer
	arm_status="$arm_status $arm=$rc"
	[ "$rc" -eq 0 ] || failed=1
done

# The first arm is compared with each other arm, and every arm is reported.
report_rc=0
set --
first=
missing=
for arm in $ARM_LIST; do
	[ -f "result-q1-$arm.json" ] || missing=1
	set -- "$@" --arm "$arm" "$ARMS/$arm" "result-q1-$arm.json"
	if [ -z "$first" ]; then
		first="$arm"
	elif [ -z "$missing" ]; then
		caliper compare "result-q1-$first.json" "result-q1-$arm.json" || true
	fi
done
if [ -z "$missing" ]; then
	vcs() { go version -m "$REPO_ROOT/bin/assistant" | sed -n "s/^[[:space:]]*build[[:space:]]*vcs.$1=//p"; }
	python3 gate_by_arms.py --revision "$(vcs revision)" --modified "$(vcs modified)" "$@" \
		>"$ARMS/gate-by-arm.json" || report_rc=$?
else
	echo "no gate verdicts by arm: an arm wrote no result file" >&2
	report_rc=1
fi
echo "results:$results" >&2
echo "gate records and usage per arm: $ARMS/<arm>/caliper-q1-gate/; gate verdicts by arm: $ARMS/gate-by-arm.json" >&2
if [ "$failed" -ne 0 ] || [ "$report_rc" -ne 0 ]; then
	echo "exit status:$arm_status gate-by-arm=$report_rc" >&2
	exit 1
fi
