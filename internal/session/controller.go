package session

import (
	"context"
	"errors"
	"fmt"
	"github.com/procly/procly/internal/config"
	"github.com/procly/procly/internal/logs"
	"github.com/procly/procly/internal/process"
	"github.com/procly/procly/internal/storage"
	"io"
	"os"
	"sync"
	"time"
)

type Controller struct {
	Store       *storage.Store
	Manager     *process.Manager
	Logs        logs.Buffer
	mu          sync.Mutex
	operations  sync.Mutex
	generations map[string]uint64
	finished    map[string]<-chan struct{}
	stopSignals map[string]chan struct{}
	outputs     map[string]process.Output
	closed      bool
	lease       *os.File
	ctx         context.Context
	cancel      context.CancelFunc
	wg          sync.WaitGroup
	closeOnce   sync.Once
	closeErr    error
}

func Open(store *storage.Store) (*Controller, error) {
	d, e := store.Load()
	if e != nil {
		return nil, e
	}
	lease, e := store.Own()
	if e != nil {
		return nil, errors.New("a Procly session already owns this data directory; use its dashboard or procly ps")
	}
	ctx, cancel := context.WithCancel(context.Background())
	c := &Controller{Store: store, Manager: process.New(store, d.Settings), generations: map[string]uint64{}, finished: map[string]<-chan struct{}{}, stopSignals: map[string]chan struct{}{}, outputs: map[string]process.Output{}, lease: lease, ctx: ctx, cancel: cancel}
	return c, nil
}
func (c *Controller) Start(name string, output process.Output) (*process.Session, error) {
	c.operations.Lock()
	defer c.operations.Unlock()
	return c.start(name, output)
}
func (c *Controller) start(name string, output process.Output) (*process.Session, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, errors.New("session is closing")
	}
	d, e := c.Store.Load()
	if e != nil {
		return nil, e
	}
	a, ok := d.Aliases[name]
	if !ok {
		return nil, fmt.Errorf("no saved command %q", name)
	}
	originalOutput := output
	out := c.Logs.Writer(name, "stdout")
	errOut := c.Logs.Writer(name, "stderr")
	if output.Stdout != nil {
		out = io.MultiWriter(out, output.Stdout)
	}
	if output.Stderr != nil {
		errOut = io.MultiWriter(errOut, output.Stderr)
	}
	output.Stdout = out
	output.Stderr = errOut
	s, e := c.Manager.Start(a, output)
	if e != nil {
		return nil, e
	}
	c.generations[name]++
	c.outputs[name] = originalOutput
	if previous := c.stopSignals[name]; previous != nil {
		select {
		case <-previous:
		default:
			close(previous)
		}
	}
	stopSignal := make(chan struct{})
	c.stopSignals[name] = stopSignal
	generation := c.generations[name]
	c.finished[name] = s.Done()
	if a.Restart == "always" || a.Restart == "on-failure" {
		done := make(chan struct{})
		c.finished[name] = done
		c.wg.Add(1)
		go func() { defer close(done); c.monitor(a, s, output, generation, stopSignal) }()
	}
	return s, nil
}
func (c *Controller) monitor(a config.Alias, s *process.Session, output process.Output, generation uint64, stopSignal <-chan struct{}) {
	defer c.wg.Done()
	for attempt := 0; attempt <= 5; attempt++ {
		select {
		case <-c.ctx.Done():
			return
		case <-stopSignal:
			return
		case <-s.Done():
		}
		r, _ := s.Wait()
		if a.Restart == "on-failure" && r.State != process.Failed {
			return
		}
		if attempt == 5 {
			fmt.Fprintln(c.Logs.Writer(a.Name, "stderr"), "Automatic restart limit reached (5). Start again to reset.")
			return
		}
		timer := time.NewTimer(time.Second * time.Duration(1<<attempt))
		select {
		case <-c.ctx.Done():
			timer.Stop()
			return
		case <-stopSignal:
			timer.Stop()
			return
		case <-timer.C:
		}
		c.mu.Lock()
		if c.closed || c.generations[a.Name] != generation {
			c.mu.Unlock()
			return
		}
		var e error
		s, e = c.Manager.Start(a, output)
		c.mu.Unlock()
		if e != nil {
			fmt.Fprintln(c.Logs.Writer(a.Name, "stderr"), "Restart failed:", e)
			return
		}
	}
}
func (c *Controller) Done(name string) <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.finished[name]
}
func (c *Controller) Stop(name string) error {
	c.operations.Lock()
	defer c.operations.Unlock()
	return c.stop(name)
}
func (c *Controller) stop(name string) error {
	c.mu.Lock()
	c.generations[name]++
	known := c.stopSignals[name] != nil
	if signal := c.stopSignals[name]; signal != nil {
		select {
		case <-signal:
		default:
			close(signal)
		}
	}
	c.mu.Unlock()
	active := false
	for _, r := range c.Manager.Snapshots() {
		if r.Alias == name && (r.State == process.Running || r.State == process.Stopping) {
			active = true
			break
		}
	}
	if !active && known {
		return nil
	}
	return c.Manager.Stop(name)
}
func (c *Controller) Restart(name string) error {
	c.operations.Lock()
	defer c.operations.Unlock()
	for _, r := range c.Manager.Snapshots() {
		if r.Alias == name && (r.State == process.Running || r.State == process.Stopping) {
			if e := c.stop(name); e != nil {
				return e
			}
			break
		}
	}
	c.mu.Lock()
	output := c.outputs[name]
	c.mu.Unlock()
	_, e := c.start(name, output)
	return e
}

// Wait observes the current generation after each completion. A remote restart
// is one operation, so an attached run cannot exit in the stop/start gap.
func (c *Controller) Wait(name string) (process.Record, error) {
	for {
		done := c.Done(name)
		if done == nil {
			return process.Record{}, fmt.Errorf("no session for %s", name)
		}
		<-done
		c.operations.Lock()
		current := c.Done(name)
		if current != done {
			c.operations.Unlock()
			continue
		}
		var latest process.Record
		for _, r := range c.Manager.Snapshots() {
			if r.Alias == name && r.Started.After(latest.Started) {
				latest = r
			}
		}
		c.operations.Unlock()
		if latest.Error != "" {
			return latest, errors.New(latest.Error)
		}
		return latest, nil
	}
}
func (c *Controller) Group(name string, stop bool) map[string]string {
	result := map[string]string{}
	d, e := c.Store.Load()
	if e != nil {
		result[name] = e.Error()
		return result
	}
	members, ok := d.Groups[name]
	if !ok {
		result[name] = "group not found"
		return result
	}
	for _, member := range members {
		if stop {
			e = c.Stop(member)
		} else {
			_, e = c.Start(member, process.Output{})
		}
		if e != nil {
			result[member] = e.Error()
		} else if stop {
			result[member] = "stopped"
		} else {
			result[member] = "started"
		}
	}
	return result
}
func (c *Controller) Close() error {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.closed = true
		c.cancel()
		c.mu.Unlock()
		c.closeErr = c.Manager.Close()
		c.wg.Wait()
		c.lease.Close()
	})
	return c.closeErr
}

// Schedules never catch up offline executions. Claims are persisted before
// launch, so interruption cannot duplicate a one-time occurrence on relaunch.
func (c *Controller) StartScheduler() {
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		started := time.Now()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-c.ctx.Done():
				return
			case now := <-ticker.C:
				c.scheduleTick(started, now)
			}
		}
	}()
}
func (c *Controller) scheduleTick(started, now time.Time) {
	d, e := c.Store.Load()
	if e != nil {
		fmt.Fprintln(c.Logs.Writer("procly", "stderr"), e)
		return
	}
	for name, s := range d.Schedules {
		if !s.Enabled {
			continue
		}
		due := s.At
		if s.Every > 0 {
			due = s.Next
		}
		if due.After(now) {
			continue
		}
		claimed := false
		missed := !due.IsZero() && due.Before(started)
		e = c.Store.Update(func(current *config.Document) error {
			live, ok := current.Schedules[name]
			if !ok || !live.Enabled || !live.Next.Equal(s.Next) || !live.At.Equal(s.At) {
				return nil
			}
			result := "claimed"
			if missed {
				result = "missed while session was closed"
			}
			if live.Every > 0 {
				live.Next = now.Add(live.Every)
			} else {
				live.Enabled = false
			}
			live.History = append(live.History, config.Execution{At: now, Result: result})
			if len(live.History) > 100 {
				live.History = live.History[len(live.History)-100:]
			}
			current.Schedules[name] = live
			claimed = !missed
			return nil
		})
		if e != nil {
			fmt.Fprintln(c.Logs.Writer("procly", "stderr"), e)
			continue
		}
		if !claimed {
			continue
		}
		running, runErr := c.Start(s.Alias, process.Output{})
		result := "started"
		if runErr != nil {
			result = runErr.Error()
		}
		_ = c.Store.Update(func(current *config.Document) error {
			live, ok := current.Schedules[name]
			if !ok {
				return nil
			}
			for i := len(live.History) - 1; i >= 0; i-- {
				if live.History[i].At.Equal(now) {
					live.History[i].Result = result
					break
				}
			}
			current.Schedules[name] = live
			return nil
		})
		if runErr == nil {
			c.wg.Add(1)
			go func(name string, at time.Time, p *process.Session) {
				defer c.wg.Done()
				r, waitErr := p.Wait()
				result := string(r.State)
				if r.ExitCode != nil {
					result += fmt.Sprintf(" (exit %d)", *r.ExitCode)
				}
				if waitErr != nil {
					result = waitErr.Error()
				}
				_ = c.Store.Update(func(d *config.Document) error {
					sc, ok := d.Schedules[name]
					if !ok {
						return nil
					}
					for i := range sc.History {
						if sc.History[i].At.Equal(at) {
							sc.History[i].Result = result
						}
					}
					d.Schedules[name] = sc
					return nil
				})
			}(name, now, running)
		}
	}
}
