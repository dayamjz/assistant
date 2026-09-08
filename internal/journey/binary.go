package journey

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
)

// BinaryVariable names the environment variable that points this harness at a
// built assistant binary.
//
// It exists so the harness can be pointed at a shipped artifact rather than at
// something it compiled itself. A harness that only ever drives a binary it
// built from the tree it is standing in has never shown that what ships works,
// which is the whole difference between validating a build and validating a
// checkout.
const BinaryVariable = "ASSISTANT_BINARY"

// productBinary is resolved once per process. Which binary is under test is a
// fact about the run rather than about a test, and building it once is the
// difference between one compile and one per test.
var productBinary struct {
	once sync.Once
	path string
	err  error
}

// Binary returns the assistant binary this harness drives.
//
// It is what BinaryVariable names when that is set, checked to exist, to not
// be a directory, and to be runnable as a program before it is returned, so
// that a misspelled path or an artifact that never had its mode bits set fails
// here rather than as every command refusing for a reason that is not the
// product's. Otherwise it is built from the module this package is in, into a
// directory that lives as long as the process.
//
// Nothing here reaches into the module's packages to do what the binary does.
// The point of driving a process is that process spawning, exit codes, stream
// separation and the home's lock behave differently across platforms, and only
// a real process finds those differences.
func Binary() (string, error) {
	productBinary.once.Do(func() {
		productBinary.path, productBinary.err = resolveBinary()
	})
	return productBinary.path, productBinary.err
}

// resolveBinary is Binary without the memoization, so the whole answer is in
// one place a reader can follow.
func resolveBinary() (string, error) {
	if named := os.Getenv(BinaryVariable); named != "" {
		info, err := os.Stat(named)
		if err != nil {
			return "", fmt.Errorf("journey: %s names %s, which cannot be read: %w", BinaryVariable, named, err)
		}
		if info.IsDir() {
			return "", fmt.Errorf("journey: %s names %s, which is a directory rather than a binary", BinaryVariable, named)
		}
		if err := executable(named, info); err != nil {
			return "", err
		}
		absolute, err := filepath.Abs(named)
		if err != nil {
			return "", fmt.Errorf("journey: resolving %s: %w", named, err)
		}
		return absolute, nil
	}
	root, err := ModuleRoot()
	if err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp("", "assistant-binary")
	if err != nil {
		return "", fmt.Errorf("journey: making a directory to build the binary into: %w", err)
	}
	built := filepath.Join(dir, "assistant"+exeSuffix())
	cmd := exec.Command("go", "build", "-o", built, "./cmd/assistant")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		// Cleanup removes this directory by way of productBinary.path, which a
		// failed build never sets, so a build that failed has to take its own
		// directory with it or nothing ever will.
		_ = os.RemoveAll(dir)
		return "", fmt.Errorf("journey: building the assistant binary from %s: %w\n%s", root, err, out)
	}
	return built, nil
}

// executable reports why a path this harness was pointed at cannot be run,
// and nil when nothing says it cannot.
//
// It is the mode bits on the platforms that decide by them, and it is
// exec.LookPath on Windows, where they decide nothing and the extension does.
// The point either way is that a path naming something unrunnable fails here,
// naming the variable that carries it, rather than as every command in the
// suite refusing for a reason that is not the product's.
func executable(named string, info os.FileInfo) error {
	if runtime.GOOS == "windows" {
		if _, err := exec.LookPath(named); err != nil {
			return fmt.Errorf("journey: %s names %s, which this platform will not run as a program: %w",
				BinaryVariable, named, err)
		}
		return nil
	}
	if info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("journey: %s names %s, whose mode %s carries no execute bit, so it is a file "+
			"rather than a binary", BinaryVariable, named, info.Mode().Perm())
	}
	return nil
}

// exeSuffix is what an executable is named on this platform.
func exeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

// ErrNoModule reports that the module this harness is part of could not be
// found from where the process is standing.
var ErrNoModule = errors.New("journey: no go.mod above the working directory")

// ModuleRoot is the root of the module this harness is part of, found by
// walking up from the working directory.
//
// A test binary runs in its own package's directory, which is inside the
// module, so this answers for a test whatever built it. It is separate from
// Binary because the fixture and the stand-in are compiled into this test
// binary from the same tree even when the product binary came from elsewhere,
// and a caller that needs the tree should say so rather than infer it from a
// binary path that may point outside it.
func ModuleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("journey: reading the working directory: %w", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", ErrNoModule
		}
		dir = parent
	}
}

// Cleanup removes what this package created for the lifetime of the process:
// the fixture it built, the copies of this binary it stood on a PATH, and the
// product binary it built when it was not given one.
//
// A consuming package calls it from TestMain after the tests have run. None of
// it belongs to any one test, so no test can clean it up, and a test binary
// that left it behind would leave tens of megabytes per run in the temporary
// directory of whatever machine ran it.
//
// It removes nothing it did not create. A binary named by BinaryVariable is
// somebody else's artifact and is left alone.
func Cleanup() {
	for _, dir := range []string{shims.dir, built.root} {
		if dir != "" {
			_ = os.RemoveAll(dir)
		}
	}
	if productBinary.path != "" && os.Getenv(BinaryVariable) == "" {
		_ = os.RemoveAll(filepath.Dir(productBinary.path))
	}
}
