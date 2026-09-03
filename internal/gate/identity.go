package gate

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

// identifierLength is how many hex characters of the digest an identifier
// keeps. Sixty-four bits is far more than a home with a handful of working
// copies in it needs to stay collision free, and a short identifier is a
// directory name a person reads in a path.
const identifierLength = 16

// Identify returns the gate identifier for a working copy path. It is the one
// place this product computes that identifier, so that a caller resolving
// <home>/repos/<id>.git and this package agree by construction rather than by
// two implementations of the same rule staying in step.
//
// The identifier is the first identifierLength hex characters of the SHA-256
// of the path, after the path has been made absolute, cleaned, and had its
// symbolic links resolved. Resolving links is what makes two spellings of one
// directory agree; letter case is not normalized, so on a case-insensitive
// filesystem two spellings that differ only in case still disagree. doc.go
// states that gap.
//
// It returns an error when the path cannot be resolved, which includes a path
// that does not exist. An identifier for a working copy that is not there is
// not a fact this package will invent.
func Identify(workingPath string) (string, error) {
	resolved, err := resolvePath(workingPath)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(resolved))
	return hex.EncodeToString(sum[:])[:identifierLength], nil
}

// resolvePath returns the absolute, cleaned, symlink-resolved form of path.
func resolvePath(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("%w: path is empty", ErrInvalidSpec)
	}
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("%w: path %q is not absolute", ErrInvalidSpec, path)
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		return "", fmt.Errorf("gate: resolving %q: %w", path, err)
	}
	return resolved, nil
}

// resolveExisting returns an absolute path with every symbolic link in it
// resolved as far as the path exists, and the rest of it appended unchanged. A
// home is allowed not to exist yet, so it cannot be resolved the way a working
// copy is.
//
// Resolving as far as the path goes, rather than only when the whole of it is
// there, is what makes the answer the same before and after the home is
// created. A home under a symbolically linked prefix that resolved to one
// spelling on the run that created it and another on every run afterwards
// would put one spelling in the working copy's remote and compare the other
// against it, so Remove would refuse a gate this package itself made.
func resolveExisting(path string) string {
	cleaned := filepath.Clean(path)
	if resolved, err := filepath.EvalSymlinks(cleaned); err == nil {
		return resolved
	}
	parent := filepath.Dir(cleaned)
	if parent == cleaned {
		// The root of a volume, which cannot be resolved and has no parent to
		// fall back to.
		return cleaned
	}
	return filepath.Join(resolveExisting(parent), filepath.Base(cleaned))
}

// repositoriesDir is where a home keeps its gate repositories, per PRD
// section 8's on-disk layout.
func repositoriesDir(home string) string {
	return filepath.Join(home, "repos")
}

// repositoryPath is where the gate with this identifier lives in this home.
func repositoryPath(home, id string) string {
	return filepath.Join(repositoriesDir(home), id+".git")
}

// isDirectory reports whether path exists and is a directory.
func isDirectory(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// holdsNoRepository reports that a path this home files a gate at holds
// nothing: it is not there, or it is an empty directory. It is the one
// definition of that, so what an initialization counts as creating a
// repository and what a caller counts as a gate that is gone cannot disagree.
func holdsNoRepository(path string) bool {
	return !isDirectory(path) || isEmptyDirectory(path)
}

// isEmptyDirectory reports whether path is a directory with nothing in it.
// InitBare accepts an empty directory, so an initialization that died between
// creating the directory and creating the repository is still repairable, and
// the repository it then creates is one this package created.
func isEmptyDirectory(path string) bool {
	entries, err := os.ReadDir(path)
	return err == nil && len(entries) == 0
}
