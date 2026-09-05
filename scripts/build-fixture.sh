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

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
exec "${GO:-go}" run "$root/cmd/fixture" -out "$1"
