package process

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/procly/procly/internal/config"
	"github.com/procly/procly/internal/logs"
	"github.com/procly/procly/internal/platform"
	"github.com/procly/procly/internal/storage"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

type State string

const (
	Starting State = "starting"
	Running  State = "running"
	Stopping State = "stopping"
	Stopped  State = "stopped"
	Failed   State = "failed"
)

type Record struct {
	MemoryBytes uint64        `json:"memory_bytes,omitempty"`
	CPUTime     time.Duration `json:"cpu_time_ns,omitempty"`
	ID          string        `json:"id"`
	Alias       string        `json:"alias"`
	PID         int           `json:"pid"`
	Started     time.Time     `json:"started"`
	Ended       time.Time     `json:"ended"`
	State       State         `json:"state"`
	ExitCode    *int          `json:"exit_code,omitempty"`
	Error       string        `json:"error,omitempty"`
	LogBackups  int           `json:"log_backups"`
}
type Session struct {
	mu       sync.Mutex
	record   Record
	child    *platform.Child
	done     chan struct{}
	stopOnce sync.Once
	err      error
}

func (s *Session) Snapshot() Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.record
	if s.child != nil && r.State == Running {
		r.MemoryBytes, r.CPUTime = s.child.Metrics()
	}
	if r.ExitCode != nil {
		code := *r.ExitCode
		r.ExitCode = &code
	}
	return r
}
func (s *Session) Done() <-chan struct{} { return s.done }
func (s *Session) Wait() (Record, error) {
	<-s.done
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.record, s.err
}
func (s *Session) Stop(timeout time.Duration) error {
	s.stopOnce.Do(func() {
		s.mu.Lock()
		if s.record.State != Running {
			s.mu.Unlock()
			return
		}
		s.record.State = Stopping
		s.mu.Unlock()
		// Some programs have no console or ignore CTRL_BREAK. The job is the backstop.
		_ = s.child.Interrupt()
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		select {
		case <-s.done:
			return
		case <-timer.C:
		}
		if e := s.child.Kill(); e != nil {
			s.mu.Lock()
			s.err = errors.Join(s.err, fmt.Errorf("terminate job: %w", e))
			s.mu.Unlock()
		}
	})
	select {
	case <-s.done:
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.err
	case <-time.After(10 * time.Second):
		return errors.New("process cleanup exceeded 10 seconds")
	}
}

type Manager struct {
	mu       sync.Mutex
	store    *storage.Store
	settings config.Settings
	sessions map[string]*Session
	closed   bool
}

func New(store *storage.Store, settings config.Settings) *Manager {
	return &Manager{store: store, settings: settings, sessions: map[string]*Session{}}
}

type Output struct {
	Stdout, Stderr io.Writer
	Stdin          *os.File
}

func (m *Manager) Start(alias config.Alias, output Output) (*Session, error) {
	if e := m.settings.Validate(); e != nil {
		return nil, e
	}
	if e := alias.ValidateLaunch(); e != nil {
		return nil, e
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, errors.New("session manager is closed")
	}
	// Bound completed runtime history too, for long-lived future dashboards.
	if len(m.sessions) >= m.settings.RetainSessions {
		var completed []*Session
		for _, old := range m.sessions {
			select {
			case <-old.done:
				completed = append(completed, old)
			default:
			}
		}
		sort.Slice(completed, func(i, j int) bool { return completed[i].Snapshot().Started.Before(completed[j].Snapshot().Started) })
		for _, old := range completed {
			if len(m.sessions) < m.settings.RetainSessions {
				break
			}
			delete(m.sessions, old.Snapshot().ID)
		}
	}
	for _, s := range m.sessions {
		r := s.Snapshot()
		if r.Alias == alias.Name && (r.State == Running || r.State == Stopping || r.State == Starting) {
			return nil, fmt.Errorf("%s is already active in this session", alias.Name)
		}
	}
	nonce := make([]byte, 12)
	if _, e := rand.Read(nonce); e != nil {
		return nil, e
	}
	// Path-safe, chronologically sortable IDs with 96 random bits.
	id := fmt.Sprintf("%019d-%s", time.Now().UnixNano(), hex.EncodeToString(nonce))
	dir, lease, e := m.store.HoldSession(id)
	if e != nil {
		return nil, e
	}
	outLog, e := logs.New(filepath.Join(dir, "stdout.log"), m.settings.LogBytes, m.settings.LogBackups)
	if e != nil {
		lease.Close()
		return nil, e
	}
	errLog, e := logs.New(filepath.Join(dir, "stderr.log"), m.settings.LogBytes, m.settings.LogBackups)
	if e != nil {
		outLog.Close()
		lease.Close()
		return nil, e
	}
	s := &Session{record: Record{ID: id, Alias: alias.Name, Started: time.Now(), State: Starting, LogBackups: m.settings.LogBackups}, done: make(chan struct{})}
	child, e := platform.Start(alias.Executable, alias.Args, alias.Directory, alias.Shell, output.Stdin)
	if e != nil {
		outLog.Close()
		errLog.Close()
		s.record.State = Failed
		s.record.Ended = time.Now()
		s.record.Error = e.Error()
		saveErr := storage.WriteJSON(filepath.Join(dir, "session.json"), s.record)
		lease.Close()
		return nil, errors.Join(e, saveErr, m.store.Prune(m.settings.RetainSessions))
	}
	s.child = child
	s.record.PID = child.PID
	s.record.State = Running
	m.sessions[id] = s
	go m.wait(s, dir, lease, outLog, errLog, output)
	return s, nil
}

// capture continues draining on a sink error, preventing a full pipe from
// deadlocking process cleanup. Errors are reported in the completed record.
func capture(src io.Reader, disk, live io.Writer) error {
	var result error
	buffer := make([]byte, 32*1024)
	for {
		n, e := src.Read(buffer)
		if n > 0 {
			if disk != nil {
				if _, err := disk.Write(buffer[:n]); err != nil {
					result = errors.Join(result, err)
					disk = nil
				}
			}
			if live != nil {
				if _, err := live.Write(buffer[:n]); err != nil {
					result = errors.Join(result, err)
					live = nil
				}
			}
		}
		if e == io.EOF {
			return result
		}
		if e != nil {
			return errors.Join(result, e)
		}
	}
}
func (m *Manager) wait(s *Session, dir string, lease *os.File, outLog, errLog *logs.Writer, output Output) {
	captures := make(chan error, 2)
	go func() { defer s.child.Stdout.Close(); captures <- capture(s.child.Stdout, outLog, output.Stdout) }()
	go func() { defer s.child.Stderr.Close(); captures <- capture(s.child.Stderr, errLog, output.Stderr) }()
	code, err := s.child.Wait()
	err = errors.Join(err, <-captures, <-captures, outLog.Close(), errLog.Close())
	s.mu.Lock()
	err = errors.Join(err, s.err)
	wasStopping := s.record.State == Stopping
	s.record.Ended = time.Now()
	s.record.ExitCode = &code
	s.record.State = Stopped
	if (!wasStopping && code != 0) || err != nil {
		s.record.State = Failed
	}
	if err != nil {
		s.record.Error = err.Error()
	}
	err = errors.Join(err, storage.WriteJSON(filepath.Join(dir, "session.json"), s.record))
	s.err = err
	s.mu.Unlock()
	lease.Close()
	// Report retention failures rather than silently allowing unbounded growth.
	pruneErr := m.store.Prune(m.settings.RetainSessions)
	s.mu.Lock()
	s.err = errors.Join(s.err, pruneErr)
	s.mu.Unlock()
	close(s.done)
}
func (m *Manager) Snapshots() []Record {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Record, 0, len(m.sessions))
	for _, s := range m.sessions {
		out = append(out, s.Snapshot())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Started.Before(out[j].Started) })
	return out
}
func (m *Manager) Stop(name string) error {
	m.mu.Lock()
	var found *Session
	for _, s := range m.sessions {
		r := s.Snapshot()
		if r.Alias == name && (r.State == Running || r.State == Stopping) {
			found = s
			break
		}
	}
	m.mu.Unlock()
	if found == nil {
		return fmt.Errorf("%s is not running in this session", name)
	}
	return found.Stop(m.settings.StopTimeout)
}
func (m *Manager) Restart(alias config.Alias, output Output) (*Session, error) {
	m.mu.Lock()
	var active *Session
	for _, s := range m.sessions {
		r := s.Snapshot()
		if r.Alias == alias.Name && (r.State == Running || r.State == Stopping) {
			active = s
			break
		}
	}
	m.mu.Unlock()
	if active != nil {
		if e := active.Stop(m.settings.StopTimeout); e != nil {
			return nil, e
		}
	}
	return m.Start(alias, output)
}
func (m *Manager) Close() error {
	m.mu.Lock()
	m.closed = true
	all := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		all = append(all, s)
	}
	m.mu.Unlock()
	errs := make(chan error, len(all))
	for _, s := range all {
		go func() { errs <- s.Stop(m.settings.StopTimeout) }()
	}
	var result error
	for range all {
		result = errors.Join(result, <-errs)
	}
	return result
}
