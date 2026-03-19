package fs

import (
	"fmt"
	"os"
	"path/filepath"
)

// ValidatedReadTarget stores a resolved regular file path for rooted re-opens.
type ValidatedReadTarget struct {
	resolvedPath string
	rootDir      string
	rootName     string
}

// NewValidatedReadTarget returns a rooted target for a resolved regular file.
func NewValidatedReadTarget(resolvedPath string) (ValidatedReadTarget, error) {
	cleanedPath := filepath.Clean(resolvedPath)
	if !filepath.IsAbs(cleanedPath) {
		return ValidatedReadTarget{}, fmt.Errorf("validated read target requires absolute path: %s", cleanedPath)
	}

	info, err := os.Lstat(cleanedPath)
	if err != nil {
		return ValidatedReadTarget{}, fmt.Errorf("lstat validated read target %s: %w", cleanedPath, err)
	}
	if !info.Mode().IsRegular() {
		return ValidatedReadTarget{}, fmt.Errorf("validated read target must be a regular file: %s", cleanedPath)
	}

	return ValidatedReadTarget{
		resolvedPath: cleanedPath,
		rootDir:      filepath.Dir(cleanedPath),
		rootName:     filepath.Base(cleanedPath),
	}, nil
}

// Open re-opens the validated file beneath its resolved parent directory.
func (t ValidatedReadTarget) Open() (*os.File, error) {
	root, err := os.OpenRoot(t.rootDir)
	if err != nil {
		return nil, fmt.Errorf("open root for %s: %w", t.resolvedPath, err)
	}
	defer root.Close()

	if err := t.validateEntry(root); err != nil {
		return nil, err
	}

	fd, err := root.Open(t.rootName)
	if err != nil {
		return nil, fmt.Errorf("open rooted file %s: %w", t.resolvedPath, err)
	}

	if err := validateOpenedFile(fd, t.resolvedPath); err != nil {
		fd.Close()
		return nil, err
	}
	if err := t.validateEntry(root); err != nil {
		fd.Close()
		return nil, err
	}

	return fd, nil
}

func (t ValidatedReadTarget) validateEntry(root *os.Root) error {
	info, err := root.Lstat(t.rootName)
	if err != nil {
		return fmt.Errorf("lstat rooted file %s: %w", t.resolvedPath, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("rooted file changed to symlink: %s", t.resolvedPath)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("rooted file changed to non-regular file: %s", t.resolvedPath)
	}
	return nil
}

func validateOpenedFile(fd *os.File, resolvedPath string) error {
	info, err := fd.Stat()
	if err != nil {
		return fmt.Errorf("stat opened file %s: %w", resolvedPath, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("opened file is not regular: %s", resolvedPath)
	}
	return nil
}
