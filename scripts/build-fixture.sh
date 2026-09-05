#!/bin/sh
# Builds the adversarial fixture repository into the directory named by the
# first argument, and prints the path of the manifest describing it.
#
# The fixture is built from nothing on every run. Nothing about it is checked
# in, so there is no git object graph in this repository for a test to depend
# on and none that cannot be regenerated.
#
# Usage: scripts/build-fixture.sh DIR
set -eu

if [ "$#" -ne 1 ]; then
	echo "usage: $0 DIR" >&2
	exit 2
fi

# The output directory is resolved before the working directory moves, because
# `go run` has to be invoked from inside this module and a relative DIR would
# then be resolved against the module root rather than against where the caller
# stands. This script exists so the fixture can be built by hand and inspected
# from wherever the caller happens to be standing, so a relative path that has
# nothing to do with the module root is the ordinary case rather than the odd
# one. The end-to-end harness does not go through here: it lives in this module
# and imports internal/fixture directly.
out_parent=$(dirname -- "$1")
out_name=$(basename -- "$1")
if ! out_parent=$(CDPATH= cd -- "$out_parent" 2>/dev/null && pwd); then
	echo "$0: $1 cannot be resolved: its parent directory does not exist" >&2
	exit 2
fi

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
CDPATH= cd -- "$root"
exec "${GO:-go}" run ./cmd/fixture -out "$out_parent/$out_name"
