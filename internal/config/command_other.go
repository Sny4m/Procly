//go:build !windows

package config

import (
	"errors"
	"strings"
)

func ParseCommand(string) (string, []string, error) { return "", nil, errors.New("Windows only") }
func CommandLine(a Alias) string                    { return strings.Join(append([]string{a.Executable}, a.Args...), " ") }
