//go:build windows

package platform

import (
	"errors"
	"fmt"
	"golang.org/x/sys/windows"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
	"unsafe"
)

// Job list is documented by the Windows SDK but not yet named in x/sys.
const procThreadAttributeJobList = 0x0002000d

type Child struct {
	PID            int
	Stdout, Stderr *os.File
	mu             sync.Mutex
	job, process   windows.Handle
	finished       bool
}

// Metrics reports root-process working set and cumulative CPU time. Child-tree
// ownership and cleanup do not depend on these optional observations.
func (c *Child) Metrics() (uint64, time.Duration) {
	if !c.mu.TryLock() {
		return 0, 0
	}
	defer c.mu.Unlock()
	if c.finished {
		return 0, 0
	}
	var info struct {
		Size, Faults                                                                       uint32
		Peak, WorkingSet, PagedPeak, Paged, NonpagedPeak, Nonpaged, Pagefile, PagefilePeak uintptr
	}
	info.Size = uint32(unsafe.Sizeof(info))
	proc := windows.NewLazySystemDLL("kernel32.dll").NewProc("K32GetProcessMemoryInfo")
	ok, _, _ := proc.Call(uintptr(c.process), uintptr(unsafe.Pointer(&info)), uintptr(info.Size))
	var memory uint64
	if ok != 0 {
		memory = uint64(info.WorkingSet)
	}
	var created, exited, kernel, user windows.Filetime
	var cpu time.Duration
	if windows.GetProcessTimes(c.process, &created, &exited, &kernel, &user) == nil {
		ticks := (uint64(kernel.HighDateTime)<<32 | uint64(kernel.LowDateTime)) + (uint64(user.HighDateTime)<<32 | uint64(user.LowDateTime))
		cpu = time.Duration(ticks) * 100 * time.Nanosecond
	}
	return memory, cpu
}

// Start attaches the child to its kill-on-close job atomically during creation.
// Only console handles are inherited; the job handle must NEVER be inherited.
func Start(executable string, args []string, dir string, shell bool, stdin *os.File) (child *Child, err error) {
	var line string
	if shell {
		system, e := windows.GetSystemDirectory()
		if e != nil {
			return nil, e
		}
		line = `cmd.exe /d /s /c "` + executable + `"`
		executable = filepath.Join(system, "cmd.exe")
	} else {
		if strings.ContainsAny(executable, `\/`) && !filepath.IsAbs(executable) {
			executable = filepath.Join(dir, executable)
		}
		executable, err = exec.LookPath(executable)
		if err != nil {
			return nil, fmt.Errorf("find executable: %w", err)
		}
		ext := strings.ToLower(filepath.Ext(executable))
		if ext == ".cmd" || ext == ".bat" {
			return nil, errors.New("batch files require explicit --shell mode (for npm use --shell -- \"npm run dev\")")
		}
		line = windows.ComposeCommandLine(append([]string{executable}, args...))
	}
	app, e := windows.UTF16PtrFromString(executable)
	if e != nil {
		return nil, e
	}
	command, e := windows.UTF16PtrFromString(line)
	if e != nil {
		return nil, e
	}
	cwd, e := windows.UTF16PtrFromString(dir)
	if e != nil {
		return nil, e
	}
	job, e := windows.CreateJobObject(nil, nil)
	if e != nil {
		return nil, e
	}
	defer func() {
		if err != nil {
			windows.CloseHandle(job)
		}
	}()
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, e = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); e != nil {
		return nil, fmt.Errorf("configure process ownership: %w", e)
	}
	outR, outW, e := os.Pipe()
	if e != nil {
		return nil, e
	}
	defer outW.Close()
	defer func() {
		if err != nil {
			outR.Close()
		}
	}()
	errR, errW, e := os.Pipe()
	if e != nil {
		return nil, e
	}
	defer errW.Close()
	defer func() {
		if err != nil {
			errR.Close()
		}
	}()
	if stdin == nil {
		stdin, e = os.Open(os.DevNull)
		if e != nil {
			return nil, e
		}
		defer stdin.Close()
	}
	// Duplicate rather than changing the caller's inheritance flags.
	handles := make([]windows.Handle, 3)
	defer func() {
		for _, h := range handles {
			if h != 0 {
				windows.CloseHandle(h)
			}
		}
	}()
	for i, f := range []*os.File{stdin, outW, errW} {
		if e = windows.DuplicateHandle(windows.CurrentProcess(), windows.Handle(f.Fd()), windows.CurrentProcess(), &handles[i], 0, true, windows.DUPLICATE_SAME_ACCESS); e != nil {
			return nil, e
		}
	}
	attributes, e := windows.NewProcThreadAttributeList(2)
	if e != nil {
		return nil, e
	}
	defer attributes.Delete()
	if e = attributes.Update(windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST, unsafe.Pointer(&handles[0]), uintptr(len(handles))*unsafe.Sizeof(handles[0])); e != nil {
		return nil, e
	}
	if e = attributes.Update(procThreadAttributeJobList, unsafe.Pointer(&job), unsafe.Sizeof(job)); e != nil {
		return nil, fmt.Errorf("atomic job assignment requires Windows 10 or later: %w", e)
	}
	si := windows.StartupInfoEx{StartupInfo: windows.StartupInfo{Cb: uint32(unsafe.Sizeof(windows.StartupInfoEx{})), Flags: windows.STARTF_USESTDHANDLES | windows.STARTF_USESHOWWINDOW, ShowWindow: windows.SW_HIDE, StdInput: handles[0], StdOutput: handles[1], StdErr: handles[2]}, ProcThreadAttributeList: attributes.List()}
	pi := windows.ProcessInformation{}
	flags := uint32(windows.CREATE_UNICODE_ENVIRONMENT | windows.EXTENDED_STARTUPINFO_PRESENT | windows.CREATE_NEW_PROCESS_GROUP)
	// No CREATE_NEW_CONSOLE: share the existing terminal, including CTRL_BREAK.
	if e = windows.CreateProcess(app, command, nil, nil, true, flags, nil, cwd, &si.StartupInfo, &pi); e != nil {
		return nil, fmt.Errorf("create owned process: %w", e)
	}
	runtime.KeepAlive(handles)
	runtime.KeepAlive(job)
	windows.CloseHandle(pi.Thread)
	return &Child{PID: int(pi.ProcessId), Stdout: outR, Stderr: errR, job: job, process: pi.Process}, nil
}

func (c *Child) Interrupt() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.finished {
		return nil
	}
	// Holding the original process handle prevents PID reuse until cleanup.
	return windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, uint32(c.PID))
}
func (c *Child) Kill() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.finished {
		return nil
	}
	return windows.TerminateJobObject(c.job, 1)
}

// Wait must be called exactly once. It also removes descendants left by a root
// that exits before its children; completion means the entire job is empty.
func (c *Child) Wait() (int, error) {
	_, waitErr := windows.WaitForSingleObject(c.process, windows.INFINITE)
	c.mu.Lock()
	defer c.mu.Unlock()
	var code uint32
	codeErr := windows.GetExitCodeProcess(c.process, &code)
	killErr := windows.TerminateJobObject(c.job, 1)
	cleanupErr := waitJobEmpty(c.job, 5*time.Second)
	c.finished = true
	closeErr := errors.Join(windows.CloseHandle(c.process), windows.CloseHandle(c.job))
	return int(code), errors.Join(waitErr, codeErr, killErr, cleanupErr, closeErr)
}
func waitJobEmpty(job windows.Handle, timeout time.Duration) error {
	// JOBOBJECT_BASIC_ACCOUNTING_INFORMATION (four LARGE_INTEGERs, four DWORDs).
	var info struct {
		User, Kernel, PeriodUser, PeriodKernel int64
		Faults, Total, Active, Terminated      uint32
	}
	deadline := time.Now().Add(timeout)
	for {
		e := windows.QueryInformationJobObject(job, windows.JobObjectBasicAccountingInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), nil)
		if e != nil {
			return e
		}
		if info.Active == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("timed out waiting for owned process tree cleanup")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
