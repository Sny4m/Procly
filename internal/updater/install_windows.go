//go:build windows

package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/procly/procly/internal/storage"
	"golang.org/x/sys/windows"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type Plan struct {
	Target, Staged, Backup, Version, SHA256 string
	Parent                                  uint32
}
type Result struct {
	Installed bool      `json:"installed"`
	Version   string    `json:"version"`
	Error     string    `json:"error,omitempty"`
	Time      time.Time `json:"time"`
}

func ValidateBinary(path, version string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "version")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	b, e := cmd.Output()
	if e != nil {
		return fmt.Errorf("validate executable: %w", e)
	}
	if strings.TrimSpace(string(b)) != "procly "+strings.TrimPrefix(version, "v") && strings.TrimSpace(string(b)) != "procly "+Version(version) {
		return errors.New("downloaded executable version does not match release")
	}
	return nil
}
func fileHash(path string) (string, error) {
	f, e := os.Open(path)
	if e != nil {
		return "", e
	}
	defer f.Close()
	h := sha256.New()
	_, e = io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), e
}
func copyFile(from, to string) error {
	src, e := os.Open(from)
	if e != nil {
		return e
	}
	defer src.Close()
	dst, e := os.OpenFile(to, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if e != nil {
		return e
	}
	_, e = io.Copy(dst, src)
	if e == nil {
		e = dst.Sync()
	}
	return errors.Join(e, dst.Close())
}
func ResultPath(target string) string {
	return filepath.Join(filepath.Dir(target), "procly-update-result.json")
}
func Stage(executable, version string) (string, error) {
	target, e := os.Executable()
	if e != nil {
		return "", e
	}
	target, e = filepath.Abs(target)
	if e != nil {
		return "", e
	}
	st, e := os.Lstat(target)
	if e != nil {
		return "", e
	}
	if st.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("update the symlink target through its original installer")
	}
	lower := strings.ToLower(target)
	for _, manager := range []string{`\scoop\`, `\chocolatey\`, `\windowsapps\`} {
		if strings.Contains(lower, manager) {
			return "", errors.New("use the original package manager to update this installation")
		}
	}
	if e = ValidateBinary(executable, version); e != nil {
		return "", e
	}
	dir, e := os.MkdirTemp(filepath.Dir(target), ".procly-update-")
	if e != nil {
		return "", fmt.Errorf("installation directory is not writable: %w", e)
	}
	success := false
	defer func() {
		if !success {
			os.RemoveAll(dir)
		}
	}()
	staged := filepath.Join(dir, "procly.exe")
	helper := filepath.Join(dir, "helper.exe")
	if e = copyFile(executable, staged); e != nil {
		return "", e
	}
	if e = copyFile(target, helper); e != nil {
		return "", e
	}
	hash, e := fileHash(staged)
	if e != nil {
		return "", e
	}
	plan := Plan{Target: target, Staged: staged, Backup: filepath.Join(dir, "previous.exe"), Version: version, SHA256: hash, Parent: uint32(os.Getpid())}
	planPath := filepath.Join(dir, "plan.json")
	if e = storage.WriteJSON(planPath, plan); e != nil {
		return "", e
	}
	cmd := exec.Command(helper, "__update-helper", planPath)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	if e = cmd.Start(); e != nil {
		return "", e
	}
	// The helper confirms it holds a handle to this exact executable before
	// the updater exits, closing the PID-reuse race for its parent wait.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, e = os.Stat(filepath.Join(dir, "ready")); e == nil {
			success = true
			cmd.Process.Release()
			return ResultPath(target), nil
		}
		time.Sleep(25 * time.Millisecond)
	}
	cmd.Process.Kill()
	cmd.Wait()
	return "", errors.New("update helper did not become ready; installed binary was not changed")
}
func Apply(plan Plan, validate func(string, string) error) error {
	actual, e := fileHash(plan.Staged)
	if e != nil {
		return e
	}
	if actual != plan.SHA256 {
		return errors.New("staged executable changed before installation")
	}
	if e = validate(plan.Staged, plan.Version); e != nil {
		return e
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		e = os.Rename(plan.Target, plan.Backup)
		if e == nil {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("executable is locked or cannot be moved: %w", e)
		}
		time.Sleep(100 * time.Millisecond)
	}
	rollback := func(cause error) error {
		if _, statErr := os.Stat(plan.Target); statErr == nil {
			if removeErr := os.Remove(plan.Target); removeErr != nil {
				return errors.Join(cause, fmt.Errorf("rollback remove failed; previous executable is at %s: %w", plan.Backup, removeErr))
			}
		}
		if restoreErr := os.Rename(plan.Backup, plan.Target); restoreErr != nil {
			return errors.Join(cause, fmt.Errorf("rollback failed; previous executable is at %s: %w", plan.Backup, restoreErr))
		}
		return cause
	}
	if e = os.Rename(plan.Staged, plan.Target); e != nil {
		return rollback(e)
	}
	if e = validate(plan.Target, plan.Version); e != nil {
		return rollback(e)
	}
	// Keep the previous executable until installation and validation succeeded.
	return os.Remove(plan.Backup)
}
func RunHelper(planPath string) error {
	data, e := os.ReadFile(planPath)
	if e != nil {
		return e
	}
	var plan Plan
	if e = json.Unmarshal(data, &plan); e != nil {
		return e
	}
	dir := filepath.Dir(planPath)
	if !filepath.IsAbs(plan.Target) || filepath.Dir(plan.Staged) != dir || filepath.Dir(plan.Backup) != dir || filepath.Dir(dir) != filepath.Dir(plan.Target) || !strings.HasPrefix(filepath.Base(dir), ".procly-update-") {
		return errors.New("invalid update staging paths")
	}
	parent, e := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, plan.Parent)
	if e != nil {
		return e
	}
	defer windows.CloseHandle(parent)
	image := make([]uint16, 32768)
	length := uint32(len(image))
	if e = windows.QueryFullProcessImageName(parent, 0, &image[0], &length); e != nil {
		return e
	}
	if !strings.EqualFold(windows.UTF16ToString(image[:length]), plan.Target) {
		return errors.New("update parent identity mismatch")
	}
	if e = os.WriteFile(filepath.Join(dir, "ready"), []byte("ready"), 0600); e != nil {
		return e
	}
	status, e := windows.WaitForSingleObject(parent, 60000)
	if e == nil && status != windows.WAIT_OBJECT_0 {
		e = errors.New("updater did not exit within one minute")
	}
	if e == nil {
		e = Apply(plan, ValidateBinary)
	}
	result := Result{Installed: e == nil, Version: plan.Version, Time: time.Now()}
	if e != nil {
		result.Error = e.Error()
	}
	writeErr := storage.WriteJSON(ResultPath(plan.Target), result)
	// A running helper cannot delete itself. Schedule only its own file for
	// deletion at reboot when permitted; leave a bounded staging directory if
	// permission is denied, with the result file explaining the outcome.
	self, _ := os.Executable()
	if e == nil {
		_ = os.Remove(planPath)
		_ = os.Remove(filepath.Join(dir, "ready"))
		p, _ := windows.UTF16PtrFromString(self)
		_ = windows.MoveFileEx(p, nil, windows.MOVEFILE_DELAY_UNTIL_REBOOT)
	}
	return errors.Join(e, writeErr)
}
