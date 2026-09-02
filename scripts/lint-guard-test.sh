#!/bin/sh
# Proves that `make lint` refuses instead of quietly becoming a weaker check.
#
# The failure this guards against is silent degradation: a contributor with no
# golangci-lint, or with one from a major series that cannot read the schema
# version 2 .golangci.yml, would otherwise see `make check` go green with no
# lint having run. This script drives the real `make lint` under a doctored
# PATH and asserts that each of those situations exits non-zero with a message
# naming the problem.
#
# It refuses rather than reporting a pass when it cannot set a case up, because
# a guard test that can pass without testing anything is the same defect again.
#
# Run it with `make lint-guard-test`.

set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
MAKE=${MAKE:-make}

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# A PATH with every directory that offers golangci-lint removed. Building it by
# subtraction keeps the rest of the toolchain (go, make, sed, tr) reachable.
printf '%s\n' "$PATH" | tr ':' '\n' >"$tmp/path-entries"
without_linter=
while IFS= read -r dir; do
	if [ -z "$dir" ] || [ -x "$dir/golangci-lint" ]; then
		continue
	fi
	if [ -z "$without_linter" ]; then
		without_linter=$dir
	else
		without_linter=$without_linter:$dir
	fi
done <"$tmp/path-entries"

if [ -z "$without_linter" ] || (PATH=$without_linter; command -v golangci-lint >/dev/null 2>&1); then
	echo "lint-guard-test: could not build a PATH without golangci-lint; refusing to report a pass." >&2
	exit 1
fi
if ! (PATH=$without_linter; command -v go >/dev/null 2>&1); then
	mkdir -p "$tmp/toolchain"
	ln -s "$(command -v go)" "$tmp/toolchain/go"
	without_linter=$tmp/toolchain:$without_linter
fi

# Installs a fake golangci-lint whose `version` output is $2, and echoes the
# PATH that finds it first. The fake fails loudly if it is ever asked to lint,
# because reaching that point means the guard let a bad version through.
stub_path() {
	stub=$tmp/$1
	mkdir -p "$stub"
	cat >"$stub/golangci-lint" <<STUB
#!/bin/sh
if [ "\$1" = "version" ]; then
	$2
fi
echo "the stub linter was asked to run" >&2
exit 0
STUB
	chmod +x "$stub/golangci-lint"
	printf '%s' "$stub:$without_linter"
}

failures=0

# Asserts that `make lint` fails under PATH $2 and explains itself with $3.
expect_refusal() {
	label=$1
	path=$2
	needle=$3
	if output=$(cd "$root" && env PATH="$path" MAKEFLAGS= "$MAKE" --no-print-directory lint 2>&1); then
		echo "FAIL $label: make lint succeeded when it should have refused." >&2
		printf '%s\n' "$output" >&2
		failures=$((failures + 1))
		return
	fi
	case $output in
	*"$needle"*)
		echo "ok   $label"
		;;
	*)
		echo "FAIL $label: make lint refused, but without saying \"$needle\"." >&2
		printf '%s\n' "$output" >&2
		failures=$((failures + 1))
		;;
	esac
}

expect_refusal "missing linter" \
	"$without_linter" \
	"golangci-lint is required and is not on PATH"

expect_refusal "wrong major series" \
	"$(stub_path v1 'echo "golangci-lint has version v1.64.8 built from abcdef0 on 2024-01-01"; exit 0')" \
	"v2 is required, but v1 is installed"

expect_refusal "unparseable version" \
	"$(stub_path unparseable 'echo "golangci-lint (unknown build)"; exit 0')" \
	"could not parse a version"

expect_refusal "version command fails" \
	"$(stub_path broken 'echo "boom" >&2; exit 3')" \
	"'golangci-lint version' failed"

if [ "$failures" -ne 0 ]; then
	echo "lint-guard-test: $failures case(s) failed." >&2
	exit 1
fi
echo "lint-guard-test: make lint refuses in all four cases."
