package plan

import (
	"crypto/subtle"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func VerifyApprovedHash(document Document, approved string) error {
	approved = strings.TrimSpace(approved)
	if approved == "" {
		return fmt.Errorf("plan approval hash is required")
	}
	planID, err := document.ID()
	if err != nil {
		return fmt.Errorf("identify plan for approval: %w", err)
	}
	if len(approved) != len(planID) || subtle.ConstantTimeCompare([]byte(approved), []byte(planID)) != 1 {
		return fmt.Errorf("approved plan hash does not match the current plan")
	}
	return nil
}

// WriteCanonicalFile writes the exact approved bytes. Without overwrite it uses
// an atomic hard-link create so an existing file can never be replaced between
// a preflight check and rename. Temporary files are created in the destination
// directory to keep the operation on one filesystem.
func WriteCanonicalFile(path string, document Document, overwrite bool) (err error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("plan output path is required")
	}
	canonical, err := document.CanonicalJSON()
	if err != nil {
		return fmt.Errorf("canonicalize plan output: %w", err)
	}
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".fortigate-external-dns-plan-*")
	if err != nil {
		return fmt.Errorf("create temporary plan output: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("protect temporary plan output: %w", err)
	}
	if _, err := temporary.Write(canonical); err != nil {
		return fmt.Errorf("write temporary plan output: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync temporary plan output: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary plan output: %w", err)
	}
	if overwrite {
		if err := os.Rename(temporaryPath, path); err != nil {
			return fmt.Errorf("replace plan output: %w", err)
		}
		return nil
	}
	if err := os.Link(temporaryPath, path); err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("plan output already exists; explicit overwrite is required")
		}
		return fmt.Errorf("create plan output: %w", err)
	}
	return nil
}
