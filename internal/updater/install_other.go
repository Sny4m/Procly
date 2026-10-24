//go:build !windows

package updater

import (
	"errors"
	"path/filepath"
)

func Stage(string, string) (string, error) { return "", errors.New("this build supports Windows only") }
func ResultPath(target string) string {
	return filepath.Join(filepath.Dir(target), "procly-update-result.json")
}
func RunHelper(string) error { return errors.New("Windows only") }
