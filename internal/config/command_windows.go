//go:build windows

package config

import (
	"errors"
	"golang.org/x/sys/windows"
)

func ParseCommand(line string) (string, []string, error) {
	args, e := windows.DecomposeCommandLine(line)
	if e != nil {
		return "", nil, e
	}
	if len(args) == 0 || args[0] == "" {
		return "", nil, errors.New("enter an executable and optional arguments")
	}
	return args[0], args[1:], nil
}
func CommandLine(a Alias) string {
	if a.Shell {
		return a.Executable
	}
	return windows.ComposeCommandLine(append([]string{a.Executable}, a.Args...))
}
