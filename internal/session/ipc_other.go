//go:build !windows

package session

import (
	"context"
	"errors"
	"github.com/procly/procly/internal/storage"
	"net"
)

func listen(*storage.Store) (net.Listener, error)            { return nil, errors.New("Windows only") }
func dial(context.Context, *storage.Store) (net.Conn, error) { return nil, errors.New("Windows only") }
