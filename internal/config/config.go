package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const SchemaVersion = 1

var namePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)
var envPattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

type Alias struct {
	Name        string   `json:"name"`
	Executable  string   `json:"executable"`
	Args        []string `json:"args,omitempty"`
	Directory   string   `json:"directory"`
	RequiredEnv []string `json:"required_env,omitempty"`
	Shell       bool     `json:"shell,omitempty"`
	Restart     string   `json:"restart,omitempty"`
}

func ValidName(name string) bool { return namePattern.MatchString(name) }
func (a Alias) Validate() error {
	if a.Restart != "" && a.Restart != "never" && a.Restart != "on-failure" && a.Restart != "always" {
		return errors.New("restart must be never, on-failure or always")
	}
	if !ValidName(a.Name) {
		return errors.New("name must be 1–64 letters, digits, hyphens or underscores, starting with a letter or digit")
	}
	if strings.TrimSpace(a.Executable) == "" {
		return errors.New("executable is required")
	}
	for _, s := range append([]string{a.Executable, a.Directory}, a.Args...) {
		if strings.ContainsRune(s, 0) {
			return errors.New("command contains a NUL character")
		}
	}
	if !filepath.IsAbs(a.Directory) {
		return errors.New("working directory must be absolute")
	}
	for _, key := range a.RequiredEnv {
		if !envPattern.MatchString(key) {
			return fmt.Errorf("invalid environment variable name %q; save names, not secret values", key)
		}
	}
	if a.Shell && len(a.Args) != 0 {
		return errors.New("shell mode takes a single script string, not separate arguments")
	}
	return nil
}

func (a Alias) ValidateLaunch() error {
	if err := a.Validate(); err != nil {
		return err
	}
	st, err := os.Stat(a.Directory)
	if err != nil {
		return fmt.Errorf("working directory: %w", err)
	}
	if !st.IsDir() {
		return errors.New("working directory is not a directory")
	}
	for _, key := range a.RequiredEnv {
		if _, ok := os.LookupEnv(key); !ok {
			return fmt.Errorf("required environment variable %s is not set", key)
		}
	}
	return nil
}

type Settings struct {
	Theme            string        `json:"theme,omitempty"`
	Color            string        `json:"color,omitempty"`
	ASCII            bool          `json:"ascii,omitempty"`
	DefaultDirectory string        `json:"default_directory,omitempty"`
	StopTimeout      time.Duration `json:"stop_timeout_ns"`
	LogBytes         int64         `json:"log_bytes"`
	LogBackups       int           `json:"log_backups"`
	RetainSessions   int           `json:"retain_sessions"`
}

func Defaults() Settings {
	return Settings{StopTimeout: 5 * time.Second, LogBytes: 2 * 1024 * 1024, LogBackups: 2, RetainSessions: 50, Theme: "auto", Color: "auto"}
}
func (s Settings) Validate() error {
	if s.Theme != "" && s.Theme != "auto" && s.Theme != "dark" && s.Theme != "light" {
		return errors.New("theme must be auto, dark or light")
	}
	if s.Color != "" && s.Color != "auto" && s.Color != "always" && s.Color != "never" {
		return errors.New("color must be auto, always or never")
	}
	if s.DefaultDirectory != "" && !filepath.IsAbs(s.DefaultDirectory) {
		return errors.New("default directory must be absolute")
	}
	if s.StopTimeout < 100*time.Millisecond || s.StopTimeout > time.Minute {
		return errors.New("stop timeout must be between 100ms and 1m")
	}
	if s.LogBytes < 1024 || s.LogBytes > 1024*1024*1024 {
		return errors.New("log bytes must be between 1024 and 1073741824")
	}
	if s.LogBackups < 0 || s.LogBackups > 10 {
		return errors.New("log backups must be between 0 and 10")
	}
	if s.RetainSessions < 1 || s.RetainSessions > 1000 {
		return errors.New("retained sessions must be between 1 and 1000")
	}
	return nil
}

type Document struct {
	Version   int                 `json:"version"`
	Aliases   map[string]Alias    `json:"aliases"`
	Settings  Settings            `json:"settings"`
	Groups    map[string][]string `json:"groups,omitempty"`
	Schedules map[string]Schedule `json:"schedules,omitempty"`
}

type Schedule struct {
	Name    string        `json:"name"`
	Alias   string        `json:"alias"`
	At      time.Time     `json:"at,omitempty"`
	Every   time.Duration `json:"every_ns,omitempty"`
	Next    time.Time     `json:"next,omitempty"`
	Enabled bool          `json:"enabled"`
	History []Execution   `json:"history,omitempty"`
}
type Execution struct {
	At     time.Time `json:"at"`
	Result string    `json:"result"`
}

func (s Schedule) Validate() error {
	if !ValidName(s.Name) || !ValidName(s.Alias) {
		return errors.New("invalid schedule or command name")
	}
	if (s.At.IsZero()) == (s.Every == 0) {
		return errors.New("choose exactly one of --at or --every")
	}
	if s.Every != 0 && s.Every < time.Second {
		return errors.New("minimum interval is 1s")
	}
	return nil
}

func New() Document {
	return Document{Version: SchemaVersion, Aliases: map[string]Alias{}, Settings: Defaults(), Groups: map[string][]string{}, Schedules: map[string]Schedule{}}
}
func (d *Document) Normalize() {
	if d.Groups == nil {
		d.Groups = map[string][]string{}
	}
	if d.Schedules == nil {
		d.Schedules = map[string]Schedule{}
	}
}
func (d Document) Validate() error {
	for name, members := range d.Groups {
		if !ValidName(name) {
			return errors.New("invalid group name")
		}
		seen := map[string]bool{}
		for _, member := range members {
			if _, ok := d.Aliases[member]; !ok {
				return fmt.Errorf("group %s: command %s does not exist", name, member)
			}
			if seen[member] {
				return fmt.Errorf("duplicate group member %s", member)
			}
			seen[member] = true
		}
	}
	for name, s := range d.Schedules {
		if name != s.Name {
			return errors.New("schedule key mismatch")
		}
		if e := s.Validate(); e != nil {
			return e
		}
		if _, ok := d.Aliases[s.Alias]; !ok {
			return fmt.Errorf("schedule %s: command %s does not exist", name, s.Alias)
		}
	}
	if d.Version != SchemaVersion {
		return fmt.Errorf("unsupported configuration schema %d", d.Version)
	}
	if d.Aliases == nil {
		return errors.New("configuration has no aliases map")
	}
	if err := d.Settings.Validate(); err != nil {
		return err
	}
	for name, a := range d.Aliases {
		if name != a.Name {
			return errors.New("alias key and name differ")
		}
		if err := a.Validate(); err != nil {
			return fmt.Errorf("alias %s: %w", name, err)
		}
	}
	return nil
}
