//go:build !windows

package storage

import (
	"errors"
	"os"
)

func lockFile(string, bool) (*os.File, error) {
	return nil, errors.New("Procly currently supports Windows only")
}
func replaceFile(string, string) error { return errors.New("Procly currently supports Windows only") }
