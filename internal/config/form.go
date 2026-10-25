package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const CommandHelp = "Enter the full command, including arguments. Use shell mode for npm, pipes and &&."

// CommandFields and CommandValues keep guided setup identical in both interfaces.
func CommandFields() []string {
	return []string{"Name", "Command", "Mode: direct or shell", "Working directory", "Required environment variables (comma separated)", "Restart: never, on-failure or always"}
}

func CommandValues(a Alias, settings Settings) ([]string, error) {
	dir := a.Directory
	if dir == "" {
		dir = settings.DefaultDirectory
	}
	if dir == "" {
		var err error
		dir, err = os.Getwd()
		if err != nil {
			return nil, err
		}
	}
	mode := "direct"
	if a.Shell {
		mode = "shell"
	}
	restart := a.Restart
	if restart == "" {
		restart = "never"
	}
	line := ""
	if a.Executable != "" {
		line = CommandLine(a)
	}
	return []string{a.Name, line, mode, dir, strings.Join(a.RequiredEnv, ","), restart}, nil
}

func CommandFromValues(values []string) (Alias, error) {
	if len(values) != len(CommandFields()) {
		return Alias{}, fmt.Errorf("incomplete command setup")
	}
	a := Alias{Name: strings.TrimSpace(values[0]), Restart: strings.TrimSpace(values[5]), RequiredEnv: SplitNames(values[4])}
	var err error
	a.Directory, err = filepath.Abs(values[3])
	if err != nil {
		return a, err
	}
	switch strings.ToLower(strings.TrimSpace(values[2])) {
	case "shell":
		a.Shell, a.Executable = true, values[1]
	case "direct":
		a.Executable, a.Args, err = ParseCommand(values[1])
		if err != nil {
			return a, err
		}
	default:
		return a, fmt.Errorf("mode must be direct or shell")
	}
	return a, a.ValidateLaunch()
}

func SplitNames(value string) []string {
	var names []string
	for _, name := range strings.Split(value, ",") {
		if name = strings.TrimSpace(name); name != "" {
			names = append(names, name)
		}
	}
	return names
}
