//go:build windows

package session

import (
	"context"
	"crypto/sha256"
	"fmt"
	"github.com/Microsoft/go-winio"
	"github.com/procly/procly/internal/storage"
	"golang.org/x/sys/windows"
	"net"
	"path/filepath"
	"strings"
)

func pipeName(s *storage.Store) string {
	hash := sha256.Sum256([]byte(strings.ToLower(filepath.Clean(s.DataDir))))
	return fmt.Sprintf(`\\.\pipe\procly-%x`, hash[:16])
}
func listen(s *storage.Store) (net.Listener, error) {
	token := windows.GetCurrentProcessToken()
	user, e := token.GetTokenUser()
	if e != nil {
		return nil, e
	}
	return winio.ListenPipe(pipeName(s), &winio.PipeConfig{SecurityDescriptor: "D:P(A;;GA;;;" + user.User.Sid.String() + ")", InputBufferSize: 65536, OutputBufferSize: 65536})
}
func dial(ctx context.Context, s *storage.Store) (net.Conn, error) {
	return winio.DialPipeContext(ctx, pipeName(s))
}
