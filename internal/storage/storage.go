package storage

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/procly/procly/internal/config"
	"io"
	"os"
	"path/filepath"
	"sort"
)

type Store struct{ ConfigDir, DataDir string }

func Open(override string) (*Store, error) {
	var s Store
	if override != "" {
		p, e := filepath.Abs(override)
		if e != nil {
			return nil, e
		}
		s = Store{p, p}
	} else {
		c, e := os.UserConfigDir()
		if e != nil {
			return nil, e
		}
		d := os.Getenv("LOCALAPPDATA")
		if d == "" {
			d = c
		}
		s = Store{filepath.Join(c, "procly"), filepath.Join(d, "procly")}
	}
	for _, p := range []string{s.ConfigDir, s.SessionsDir()} {
		if e := os.MkdirAll(p, 0700); e != nil {
			return nil, e
		}
	}
	return &s, nil
}
func (s *Store) SessionsDir() string { return filepath.Join(s.DataDir, "sessions") }
func (s *Store) Load() (config.Document, error) {
	f, e := os.Open(filepath.Join(s.ConfigDir, "config.json"))
	if errors.Is(e, os.ErrNotExist) {
		return config.New(), nil
	}
	if e != nil {
		return config.Document{}, e
	}
	defer f.Close()
	var d config.Document
	dec := json.NewDecoder(io.LimitReader(f, 4*1024*1024))
	dec.DisallowUnknownFields()
	if e = dec.Decode(&d); e != nil {
		return d, fmt.Errorf("read config.json (file preserved): %w", e)
	}
	var extra any
	if e = dec.Decode(&extra); e != io.EOF {
		return d, errors.New("config.json contains trailing data")
	}
	d.Normalize()
	return d, d.Validate()
}

// Own grants one foreground supervisor per data directory. OS locks disappear
// on process death, unlike PID files. Queries never launch a supervisor.
func (s *Store) Own() (*os.File, error) {
	return lockFile(filepath.Join(s.DataDir, "owner.lock"), true)
}
func (s *Store) Update(fn func(*config.Document) error) error {
	lock, e := lockFile(filepath.Join(s.ConfigDir, "config.lock"), false)
	if e != nil {
		return e
	}
	defer lock.Close()
	d, e := s.Load()
	if e != nil {
		return e
	}
	if e = fn(&d); e != nil {
		return e
	}
	if e = d.Validate(); e != nil {
		return e
	}
	return WriteJSON(filepath.Join(s.ConfigDir, "config.json"), d)
}
func WriteJSON(path string, value any) error {
	b, e := json.MarshalIndent(value, "", "  ")
	if e != nil {
		return e
	}
	b = append(b, '\n')
	f, e := os.CreateTemp(filepath.Dir(path), ".procly-*")
	if e != nil {
		return e
	}
	name := f.Name()
	defer os.Remove(name)
	if _, e = f.Write(b); e != nil {
		f.Close()
		return e
	}
	if e = f.Sync(); e != nil {
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	return replaceFile(name, path)
}

// HoldSession keeps pruning from deleting a live session, even across invocations.
func (s *Store) HoldSession(id string) (string, *os.File, error) {
	if !config.ValidName(id) {
		return "", nil, errors.New("invalid session ID")
	}
	retention, e := lockFile(filepath.Join(s.DataDir, "retention.lock"), false)
	if e != nil {
		return "", nil, e
	}
	defer retention.Close()
	dir := filepath.Join(s.SessionsDir(), id)
	if e := os.Mkdir(dir, 0700); e != nil {
		return "", nil, e
	}
	f, e := lockFile(filepath.Join(dir, "active.lock"), false)
	return dir, f, e
}
func (s *Store) Prune(keep int) error {
	lock, e := lockFile(filepath.Join(s.DataDir, "retention.lock"), false)
	if e != nil {
		return e
	}
	defer lock.Close()
	entries, e := os.ReadDir(s.SessionsDir())
	if e != nil {
		return e
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() > entries[j].Name() })
	retained := 0
	for _, entry := range entries {
		if !entry.IsDir() || !config.ValidName(entry.Name()) {
			continue
		}
		dir := filepath.Join(s.SessionsDir(), entry.Name())
		f, e := lockFile(filepath.Join(dir, "active.lock"), true)
		if e != nil {
			continue
		}
		retained++
		if retained <= keep {
			f.Close()
			continue
		}
		// HoldSession uses the same retention lock. IDs are never reused;
		// release the lease before removing its directory on Windows.
		f.Close()
		if e = os.RemoveAll(dir); e != nil {
			return e
		}
	}
	return nil
}
