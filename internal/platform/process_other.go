//go:build !windows

package platform

import (
	"errors"
	"os"
	"time"
)

type Child struct {
	PID            int
	Stdout, Stderr *os.File
}

func (c *Child) Metrics() (uint64, time.Duration) { return 0, 0 }

func Start(string, []string, string, bool, *os.File) (*Child, error) {
	return nil, errors.New("Procly currently supports Windows only")
}
func (c *Child) Interrupt() error   { return errors.New("unsupported platform") }
func (c *Child) Kill() error        { return errors.New("unsupported platform") }
func (c *Child) Wait() (int, error) { return -1, errors.New("unsupported platform") }
