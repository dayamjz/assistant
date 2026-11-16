#!/bin/sh
# install-caliper.sh [VENV]
#
# Install the caliper run-full.sh refuses to run without: the caliper-eval
# 0.19.0 sdist from PyPI, checked against its sha256, with
# caliper-0.19.0-q1attemptenv.patch applied (README "As run"), into the
# virtual environment VENV as version 0.19.0+q1attemptenv. VENV defaults to
# $CALIPER_VENV, else $HOME/.local/share/caliper-q1attemptenv, which
# run-full.sh puts on PATH. PYTHON is a Python 3.10 or later (default python3).
set -eu
cd "$(dirname "$0")"
VENV="${1:-${CALIPER_VENV:-$HOME/.local/share/caliper-q1attemptenv}}"
PYTHON="${PYTHON:-python3}"
SDIST=caliper_eval-0.19.0
SHA256=0d289322d907cf0ae0c076129ed681d517134e746af655309581c32ec8b40f03
PATCH="$PWD/caliper-0.19.0-q1attemptenv.patch"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/caliper-q1-install.XXXXXX")"
trap 'rm -rf "${WORK:?}"' EXIT

curl -fsSL -o "$WORK/$SDIST.tar.gz" "https://files.pythonhosted.org/packages/source/c/caliper-eval/$SDIST.tar.gz"
got="$("$PYTHON" -c 'import hashlib, sys; print(hashlib.sha256(open(sys.argv[1], "rb").read()).hexdigest())' "$WORK/$SDIST.tar.gz")"
if [ "$got" != "$SHA256" ]; then
	echo "install-caliper.sh: $SDIST.tar.gz has sha256 $got, not $SHA256" >&2
	exit 1
fi
tar -xzf "$WORK/$SDIST.tar.gz" -C "$WORK"
# No fuzz and no reversed hunks: the patch is for this exact sdist.
patch -d "$WORK/$SDIST" -p1 -N -F 0 -i "$PATCH"
"$PYTHON" -m venv "$VENV"
"$VENV/bin/python" -m pip install --quiet "$WORK/$SDIST"
version="$("$VENV/bin/python" -c 'import importlib.metadata as m; print(m.version("caliper-eval"))')"
if [ "$version" != 0.19.0+q1attemptenv ]; then
	echo "install-caliper.sh: $VENV has caliper-eval $version, not 0.19.0+q1attemptenv" >&2
	exit 1
fi
echo "caliper-eval $version installed: $VENV/bin/caliper" >&2
