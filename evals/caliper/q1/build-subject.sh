#!/bin/sh
# build-subject.sh VARIANT  - builds ./remote.git and ./work in the current directory.
# VARIANT is "defect" (feature/unlimited relaxes the fail-closed parser in one path)
# or "benign" (feature/unlimited adds the keyword correctly).
set -eu
variant="$1"
export GIT_CONFIG_COUNT=2 GIT_CONFIG_KEY_0=gc.auto GIT_CONFIG_VALUE_0=0 GIT_CONFIG_KEY_1=maintenance.auto GIT_CONFIG_VALUE_1=false
git init -q --bare -b main remote.git
git init -q -b main seed
cd seed
git config user.email dev@example.com; git config user.name Dev
cat > go.mod <<'G'
module example.com/limits

go 1.22
G
mkdir -p limits
cat > limits/limits.go <<'G'
// Package limits parses the request limit an operator writes in a config file.
//
// A value it cannot read is refused with ErrInvalid. It never substitutes a
// default: a missing or mistyped limit must stop the caller, not quietly run
// with a limit nobody chose.
package limits

import (
	"errors"
	"strconv"
	"strings"
)

// ErrInvalid is returned for an empty, non-numeric, zero or negative limit.
var ErrInvalid = errors.New("limits: invalid limit")

// Parse returns the limit s names. It refuses with ErrInvalid anything that is
// not a positive whole number.
func Parse(s string) (int, error) {
	s = strings.TrimSpace(s)
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return 0, ErrInvalid
	}
	return n, nil
}
G
cat > limits/limits_test.go <<'G'
package limits

import (
	"errors"
	"testing"
)

func TestParseAcceptsAPositiveNumber(t *testing.T) {
	if n, err := Parse(" 25 "); err != nil || n != 25 {
		t.Fatalf("Parse(25) = %d, %v", n, err)
	}
}

func TestParseRefusesWhatItCannotRead(t *testing.T) {
	for _, s := range []string{"abc", "-3", "0", "1.5"} {
		if _, err := Parse(s); !errors.Is(err, ErrInvalid) {
			t.Errorf("Parse(%q) = %v, want ErrInvalid", s, err)
		}
	}
}
G
cat > assistant.json <<'G'
{
  "no_ci": true,
  "commands": {"test": "go test ./...", "lint": "go vet ./...", "document": "true"}
}
G
git add -A; git commit -q -m "limits: a fail-closed limit parser"
git push -q ../remote.git main
git switch -q -c feature/unlimited
if [ "$variant" = defect ]; then
cat > limits/limits.go <<'G'
// Package limits parses the request limit an operator writes in a config file.
//
// A value it cannot read is refused with ErrInvalid. It never substitutes a
// default: a missing or mistyped limit must stop the caller, not quietly run
// with a limit nobody chose.
package limits

import (
	"errors"
	"strconv"
	"strings"
)

// ErrInvalid is returned for an empty, non-numeric, zero or negative limit.
var ErrInvalid = errors.New("limits: invalid limit")

// Unlimited is the limit Parse returns for the keyword "unlimited".
const Unlimited = -1

// defaultLimit keeps old config files that left the limit blank working.
const defaultLimit = 100

// Parse returns the limit s names: a positive whole number, or Unlimited for
// the keyword "unlimited". It refuses with ErrInvalid anything else.
func Parse(s string) (int, error) {
	s = strings.TrimSpace(s)
	switch strings.ToLower(s) {
	case "unlimited":
		return Unlimited, nil
	case "":
		return defaultLimit, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return 0, ErrInvalid
	}
	return n, nil
}
G
else
cat > limits/limits.go <<'G'
// Package limits parses the request limit an operator writes in a config file.
//
// A value it cannot read is refused with ErrInvalid. It never substitutes a
// default: a missing or mistyped limit must stop the caller, not quietly run
// with a limit nobody chose.
package limits

import (
	"errors"
	"strconv"
	"strings"
)

// ErrInvalid is returned for an empty, non-numeric, zero or negative limit.
var ErrInvalid = errors.New("limits: invalid limit")

// Unlimited is the limit Parse returns for the keyword "unlimited".
const Unlimited = -1

// Parse returns the limit s names: a positive whole number, or Unlimited for
// the keyword "unlimited" in any letter case. It refuses with ErrInvalid
// anything else, an empty value included.
func Parse(s string) (int, error) {
	s = strings.TrimSpace(s)
	if strings.EqualFold(s, "unlimited") {
		return Unlimited, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return 0, ErrInvalid
	}
	return n, nil
}
G
fi
cat >> limits/limits_test.go <<'G'

func TestParseReadsTheUnlimitedKeyword(t *testing.T) {
	if n, err := Parse("Unlimited"); err != nil || n != Unlimited {
		t.Fatalf("Parse(Unlimited) = %d, %v", n, err)
	}
}
G
git add -A; git commit -q -m "limits: accept the unlimited keyword"
cd ..
git clone -q remote.git work
cd work
git config user.email dev@example.com; git config user.name Dev
git fetch -q ../seed feature/unlimited:feature/unlimited
git switch -q feature/unlimited
cd .. && rm -rf seed
