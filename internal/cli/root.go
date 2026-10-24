package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/procly/procly/internal/config"
	"github.com/procly/procly/internal/logs"
	"github.com/procly/procly/internal/process"
	"github.com/procly/procly/internal/session"
	"github.com/procly/procly/internal/storage"
	"github.com/procly/procly/internal/tui"
	"github.com/spf13/cobra"
	"golang.org/x/term"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"text/tabwriter"
	"time"
)

// ExitError preserves the managed command's exit status without printing a
// second error after its own stderr has already explained the failure.
type ExitError struct{ Code int }

type lockedWriter struct {
	mu     *sync.Mutex
	writer io.Writer
}

func (w lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writer.Write(p)
}

func (e ExitError) Error() string { return fmt.Sprintf("command exited with code %d", e.Code) }

func New(version string) *cobra.Command {
	var dataDir string
	root := &cobra.Command{Use: "procly [COMMAND]", Short: "Run it. Track it. Control it.", Long: "procly_  Run it. Track it. Control it.\n\nOpen your process dashboard, or run a saved command by name.\nProcesses belong to the session that starts them.", SilenceUsage: true, SilenceErrors: true, Args: cobra.MaximumNArgs(1)}
	root.SetOut(os.Stdout)
	root.SetErr(os.Stderr)
	root.SetIn(os.Stdin)
	root.PersistentFlags().StringVar(&dataDir, "data-dir", "", "Override per-user storage directories")
	store := func() (*storage.Store, error) { return storage.Open(dataDir) }
	root.AddCommand(&cobra.Command{Use: "version", Short: "Show the installed version", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error { fmt.Fprintln(c.OutOrStdout(), "procly", version); return nil }})
	root.AddCommand(saveCommand(false, store), saveCommand(true, store))
	root.AddCommand(&cobra.Command{Use: "ls", Short: "List saved commands", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		s, e := store()
		if e != nil {
			return e
		}
		d, e := s.Load()
		if e != nil {
			return e
		}
		if len(d.Aliases) == 0 {
			fmt.Fprint(c.OutOrStdout(), "\n  No commands yet.\n\n  Save the commands you use every day.\n  Get started: procly add\n\n")
			return nil
		}
		w := tabwriter.NewWriter(c.OutOrStdout(), 0, 4, 3, ' ', 0)
		fmt.Fprintln(w, "NAME\tCOMMAND\tDIRECTORY")
		for _, name := range names(d) {
			a := d.Aliases[name]
			fmt.Fprintf(w, "%s\t%s\t%s\n", name, display(a), safeText(a.Directory))
		}
		return w.Flush()
	}})
	root.AddCommand(&cobra.Command{Use: "info NAME", Short: "Inspect a saved definition (not live process state)", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, args []string) error {
		s, e := store()
		if e != nil {
			return e
		}
		a, e := lookup(s, args[0])
		if e != nil {
			return e
		}
		enc := json.NewEncoder(c.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(a)
	}})
	var yes bool
	remove := &cobra.Command{Use: "rm NAME", Short: "Remove a saved command", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, args []string) error {
		s, e := store()
		if e != nil {
			return e
		}
		if _, e = lookup(s, args[0]); e != nil {
			return e
		}
		if !yes {
			ok, e := confirm(c, fmt.Sprintf("Remove saved command %q?", args[0]))
			if e != nil {
				return e
			}
			if !ok {
				fmt.Fprintln(c.OutOrStdout(), "Cancelled.")
				return nil
			}
		}
		e = s.Update(func(d *config.Document) error {
			if _, ok := d.Aliases[args[0]]; !ok {
				return fmt.Errorf("no saved command %q", args[0])
			}
			delete(d.Aliases, args[0])
			return nil
		})
		if e == nil {
			fmt.Fprintln(c.OutOrStdout(), "Removed", args[0])
		}
		return e
	}}
	remove.Flags().BoolVarP(&yes, "yes", "y", false, "Confirm removal")
	root.AddCommand(remove)
	var timeout time.Duration
	run := &cobra.Command{Use: "run NAME", Short: "Run in this terminal; Ctrl+C stops the owned process tree", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, args []string) (result error) {
		s, e := store()
		if e != nil {
			return e
		}
		d, e := s.Load()
		if e != nil {
			return e
		}
		a, ok := d.Aliases[args[0]]
		if !ok {
			return fmt.Errorf("no saved command %q; use procly ls", args[0])
		}
		if c.Flags().Changed("stop-timeout") {
			d.Settings.StopTimeout = timeout
		}
		if e = d.Settings.Validate(); e != nil {
			return e
		}
		if e = c.Context().Err(); e != nil {
			return e
		}
		controller, e := session.Open(s)
		if e != nil {
			return e
		}
		controller.Manager = process.New(s, d.Settings)

		// A per-run timeout override affects this foreground owner's stop path.
		stopServer, e := session.Serve(controller)
		if e != nil {
			controller.Close()
			return e
		}
		defer stopServer()
		defer func() {
			if cleanup := controller.Close(); cleanup != nil {
				result = fmt.Errorf("session cleanup: %w", cleanup)
			}
		}()
		var stdin *os.File
		if f, ok := c.InOrStdin().(*os.File); ok {
			stdin = f
		}
		outputLock := &sync.Mutex{}
		stdout := lockedWriter{outputLock, c.OutOrStdout()}
		stderr := lockedWriter{outputLock, c.ErrOrStderr()}
		managed, e := controller.Start(a.Name, process.Output{Stdout: stdout, Stderr: stderr, Stdin: stdin})
		if e != nil {
			return e
		}
		type completion struct {
			record process.Record
			err    error
		}
		completed := make(chan completion, 1)
		go func() { r, e := controller.Wait(a.Name); completed <- completion{r, e} }()
		select {
		case <-c.Context().Done():
			fmt.Fprintln(stderr, "\nStopping", a.Name+"...")
			if e = managed.Stop(d.Settings.StopTimeout); e != nil {
				return e
			}
			return ExitError{130}
		case done := <-completed:
			r, e := done.record, done.err
			if e != nil {
				return e
			}
			if r.ExitCode != nil && *r.ExitCode != 0 {
				return ExitError{*r.ExitCode}
			}
			return nil
		}
	}}
	run.Flags().DurationVar(&timeout, "stop-timeout", 5*time.Second, "Grace period before terminating the owned process tree")
	root.AddCommand(run)

	root.RunE = func(c *cobra.Command, args []string) error {
		if len(args) == 1 {
			return run.RunE(c, args)
		}
		if !term.IsTerminal(int(os.Stdin.Fd())) {
			return c.Help()
		}
		s, e := store()
		if e != nil {
			return e
		}
		return tui.Run(c.Context(), s, version)
	}
	root.AddCommand(historyCommand(store), logsCommand(store), settingsCommand(store))
	root.AddCommand(groupCommand(store), scheduleCommand(store), transferCommand(store, false), transferCommand(store, true))
	root.AddCommand(remoteCommands(store)...)
	root.AddCommand(updateCommand(version))
	for _, cmd := range root.Commands() {
		switch cmd.Name() {
		case "ls":
			cmd.Aliases = []string{"list"}
		case "add":
			cmd.Aliases = []string{"save"}
		case "rm":
			cmd.Aliases = []string{"remove"}
		case "run":
			cmd.Aliases = []string{"start"}
		case "settings":
			cmd.Aliases = []string{"config"}
		}
	}
	return root
}

func names(d config.Document) []string {
	out := make([]string, 0, len(d.Aliases))
	for name := range d.Aliases {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
func lookup(s *storage.Store, name string) (config.Alias, error) {
	d, e := s.Load()
	if e != nil {
		return config.Alias{}, e
	}
	a, ok := d.Aliases[name]
	if !ok {
		return a, fmt.Errorf("no saved command %q; use procly ls", name)
	}
	return a, nil
}
func safeText(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return ' '
		}
		return r
	}, s)
}
func display(a config.Alias) string {
	if a.Shell {
		return "shell: " + safeText(a.Executable)
	}
	parts := []string{strconv.Quote(a.Executable)}
	for _, arg := range a.Args {
		parts = append(parts, strconv.Quote(arg))
	}
	return strings.Join(parts, " ")
}
func confirm(c *cobra.Command, prompt string) (bool, error) {
	fmt.Fprint(c.OutOrStdout(), prompt+" [y/N] ")
	line, e := bufio.NewReader(c.InOrStdin()).ReadString('\n')
	if e != nil {
		return false, fmt.Errorf("confirmation requires input; use --yes for automation: %w", e)
	}
	return strings.EqualFold(strings.TrimSpace(line), "y") || strings.EqualFold(strings.TrimSpace(line), "yes"), nil
}

type storeFunc func() (*storage.Store, error)

func saveCommand(edit bool, store storeFunc) *cobra.Command {
	var dir string
	var env []string
	var shell bool
	var restart string
	verb := "add"
	short := "Save a command, or open the guided creation wizard"
	if edit {
		verb = "edit"
		short = "Replace a saved command, with a review when using the wizard"
	}
	cmd := &cobra.Command{Use: verb + " [NAME] [-- EXECUTABLE ARG...]", Short: short, Example: "  procly add api -- node server.js\n  procly add api --shell -- \"npm run dev\"\n  procly add", RunE: func(c *cobra.Command, args []string) error {
		s, e := store()
		if e != nil {
			return e
		}
		var a config.Alias
		if len(args) == 0 {
			if edit {
				return errors.New("provide the command name to edit")
			}
			a, e = wizard(c, config.Alias{})
			if e != nil {
				return e
			}
		} else {
			if edit {
				a, e = lookup(s, args[0])
				if e != nil {
					return e
				}
			}
			if len(args) == 1 {
				if !edit {
					a = config.Alias{Name: args[0]}
				}
				a, e = wizard(c, a)
				if e != nil {
					return e
				}
			} else {
				if c.ArgsLenAtDash() != 1 {
					return errors.New("separate the alias and executable with -- (example: procly add api -- node server.js)")
				}
				if dir == "" {
					if edit {
						dir = a.Directory
					} else {
						dir, e = os.Getwd()
						if e != nil {
							return e
						}
					}
				}
				dir, e = filepath.Abs(dir)
				if e != nil {
					return e
				}
				if edit && !c.Flags().Changed("require-env") {
					env = a.RequiredEnv
				}
				if edit && !c.Flags().Changed("restart") {
					restart = a.Restart
				}
				a = config.Alias{Name: args[0], Executable: args[1], Args: append([]string(nil), args[2:]...), Directory: dir, RequiredEnv: env, Shell: shell, Restart: restart}
			}
		}
		if a.Name == "" {
			fmt.Fprintln(c.OutOrStdout(), "Cancelled.")
			return nil
		}
		if e = a.ValidateLaunch(); e != nil {
			return e
		}
		e = s.Update(func(d *config.Document) error {
			_, exists := d.Aliases[a.Name]
			if edit && !exists {
				return fmt.Errorf("no saved command %q", a.Name)
			}
			if !edit && exists {
				return fmt.Errorf("%q already exists; use procly edit", a.Name)
			}
			d.Aliases[a.Name] = a
			return nil
		})
		if e == nil {
			fmt.Fprintf(c.OutOrStdout(), "Saved %s. Run with: procly run %s\n", a.Name, a.Name)
		}
		return e
	}}
	cmd.Flags().StringVarP(&dir, "dir", "d", "", "Working directory (defaults to current directory)")
	cmd.Flags().StringSliceVar(&env, "require-env", nil, "Required inherited environment variable names; values are never saved")
	cmd.Flags().BoolVar(&shell, "shell", false, "Explicit cmd.exe shell mode; provide one quoted script after --")
	cmd.Flags().StringVar(&restart, "restart", "never", "Restart policy: never, on-failure or always (up to 5 retries)")
	return cmd
}

func wizard(c *cobra.Command, a config.Alias) (config.Alias, error) {
	reader := bufio.NewReader(c.InOrStdin())
	out := c.OutOrStdout()
	fmt.Fprint(out, "\n  procly_ / save command\n\n  Arguments are entered separately; no implicit shell.\n  Environment values are inherited, never saved.\n\n")
	ask := func(label, fallback string) (string, error) {
		fmt.Fprint(out, "  "+label)
		if fallback != "" {
			fmt.Fprint(out, " ["+safeText(fallback)+"]")
		}
		fmt.Fprint(out, ": ")
		v, e := reader.ReadString('\n')
		if e != nil {
			return "", fmt.Errorf("input interrupted; nothing saved: %w", e)
		}
		v = strings.TrimSuffix(strings.TrimSuffix(v, "\n"), "\r")
		if v == "" {
			return fallback, nil
		}
		return v, nil
	}
	var e error
	if a.Name == "" {
		a.Name, e = ask("Name", "")
		if e != nil {
			return a, e
		}
	}
	mode := "no"
	if a.Shell {
		mode = "yes"
	}
	mode, e = ask("Use shell syntax? yes/no", mode)
	if e != nil {
		return a, e
	}
	if mode != "yes" && mode != "no" {
		return a, errors.New("shell mode must be yes or no")
	}
	a.Shell = mode == "yes"
	label := "Executable"
	if a.Shell {
		label = "Shell script"
	}
	a.Executable, e = ask(label, a.Executable)
	if e != nil {
		return a, e
	}
	if !a.Shell {
		replace := true
		if len(a.Args) > 0 {
			fmt.Fprintln(out, "  Current arguments:", display(a))
			answer, err := ask("Replace arguments? yes/no", "no")
			if err != nil {
				return a, err
			}
			if answer != "yes" && answer != "no" {
				return a, errors.New("answer yes or no")
			}
			replace = answer == "yes"
		}
		if replace {
			a.Args = nil
			fmt.Fprintln(out, "  Enter one argument per line, without wrapping quotes. Blank ends arguments.")
			for i := 1; ; i++ {
				arg, e := ask(fmt.Sprintf("Argument %d", i), "")
				if e != nil {
					return a, e
				}
				if arg == "" {
					break
				}
				a.Args = append(a.Args, arg)
			}
		}
	} else {
		a.Args = nil
	}
	if a.Directory == "" {
		a.Directory, e = os.Getwd()
		if e != nil {
			return a, e
		}
	}
	a.Directory, e = ask("Working directory", a.Directory)
	if e != nil {
		return a, e
	}
	a.Directory, e = filepath.Abs(a.Directory)
	if e != nil {
		return a, e
	}
	keys, e := ask("Required environment names (comma separated)", strings.Join(a.RequiredEnv, ","))
	if e != nil {
		return a, e
	}
	a.RequiredEnv = nil
	if keys != "" {
		for _, key := range strings.Split(keys, ",") {
			a.RequiredEnv = append(a.RequiredEnv, strings.TrimSpace(key))
		}
	}
	if e = a.ValidateLaunch(); e != nil {
		return a, e
	}
	fmt.Fprintf(out, "\n  Review\n  Name        %s\n  Command     %s\n  Directory   %s\n  Environment %s\n  Restart     Manual (Phase 1)\n\n", a.Name, display(a), safeText(a.Directory), strings.Join(a.RequiredEnv, ", "))
	answer, e := ask("Save? y/N", "N")
	if e != nil {
		return a, e
	}
	if !strings.EqualFold(answer, "y") && !strings.EqualFold(answer, "yes") {
		return config.Alias{}, nil
	}
	return a, nil
}

func records(s *storage.Store) ([]process.Record, error) {
	entries, e := os.ReadDir(s.SessionsDir())
	if e != nil {
		return nil, e
	}
	var records []process.Record
	for _, entry := range entries {
		if !entry.IsDir() || !config.ValidName(entry.Name()) {
			continue
		}
		path := filepath.Join(s.SessionsDir(), entry.Name(), "session.json")
		f, e := os.Open(path)
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			return nil, e
		}
		var r process.Record
		e = json.NewDecoder(io.LimitReader(f, 1024*1024)).Decode(&r)
		f.Close()
		if e != nil {
			return nil, fmt.Errorf("read session %s: %w", entry.Name(), e)
		}
		if r.ID != entry.Name() || r.LogBackups < 0 || r.LogBackups > 10 {
			return nil, fmt.Errorf("invalid session record %s", entry.Name())
		}
		records = append(records, r)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Started.After(records[j].Started) })
	return records, nil
}
func historyCommand(store storeFunc) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{Use: "history [NAME]", Short: "Show completed sessions; historical PIDs are never live state", Args: cobra.MaximumNArgs(1), RunE: func(c *cobra.Command, args []string) error {
		s, e := store()
		if e != nil {
			return e
		}
		rs, e := records(s)
		if e != nil {
			return e
		}
		if asJSON {
			filtered := make([]process.Record, 0)
			for _, r := range rs {
				if len(args) == 0 || r.Alias == args[0] {
					filtered = append(filtered, r)
				}
			}
			enc := json.NewEncoder(c.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(filtered)
		}
		w := tabwriter.NewWriter(c.OutOrStdout(), 0, 4, 3, ' ', 0)
		fmt.Fprintln(w, "NAME\tSTATE\tEXIT\tSTARTED\tSESSION")
		for _, r := range rs {
			if len(args) > 0 && r.Alias != args[0] {
				continue
			}
			code := "-"
			if r.ExitCode != nil {
				code = strconv.Itoa(*r.ExitCode)
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", safeText(r.Alias), safeText(string(r.State)), code, r.Started.Local().Format(time.RFC3339), r.ID)
		}
		return w.Flush()
	}}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Show full records, including error details")
	return cmd
}
func logsCommand(store storeFunc) *cobra.Command {
	var stream, id string
	var follow bool
	cmd := &cobra.Command{Use: "logs NAME", Short: "Read saved output, or follow live logs with -f", Long: "Read output from the latest completed session. Use -f to follow the active\nsession, and --stream stderr to select error output.", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, args []string) error {
		if stream != "stdout" && stream != "stderr" {
			return errors.New("stream must be stdout or stderr")
		}
		s, e := store()
		if e != nil {
			return e
		}
		if follow {
			return followLogs(c, s, args[0], stream)
		}
		rs, e := records(s)
		if e != nil {
			return e
		}
		for _, r := range rs {
			if r.Alias == args[0] && (id == "" || r.ID == id) {
				return logs.Read(c.OutOrStdout(), filepath.Join(s.SessionsDir(), r.ID, stream+".log"), r.LogBackups)
			}
		}
		return fmt.Errorf("no completed session logs for %q; live output is attached to procly run", args[0])
	}}
	cmd.Flags().StringVar(&stream, "stream", "stdout", "stdout or stderr")
	cmd.Flags().StringVar(&id, "session", "", "Read a specific completed session ID")
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "Follow live output from the owning session")
	return cmd
}
func settingsCommand(store storeFunc) *cobra.Command {
	return &cobra.Command{Use: "settings [KEY VALUE]", Short: "Inspect or set stop-timeout, log-bytes, log-backups, retain-sessions", Args: func(_ *cobra.Command, args []string) error {
		if len(args) != 0 && len(args) != 2 {
			return errors.New("use procly settings or procly settings KEY VALUE")
		}
		return nil
	}, RunE: func(c *cobra.Command, args []string) error {
		s, e := store()
		if e != nil {
			return e
		}
		if len(args) == 0 {
			d, e := s.Load()
			if e != nil {
				return e
			}
			fmt.Fprintf(c.OutOrStdout(), "stop-timeout     %s\nlog-bytes        %d\nlog-backups      %d\nretain-sessions  %d\n", d.Settings.StopTimeout, d.Settings.LogBytes, d.Settings.LogBackups, d.Settings.RetainSessions)
			fmt.Fprintf(c.OutOrStdout(), "theme           %s\ncolor           %s\ndefault-dir     %s\n", d.Settings.Theme, d.Settings.Color, d.Settings.DefaultDirectory)
			return nil
		}
		return s.Update(func(d *config.Document) error {
			var e error
			switch args[0] {
			case "theme":
				d.Settings.Theme = args[1]
			case "color":
				d.Settings.Color = args[1]
			case "default-dir":
				d.Settings.DefaultDirectory, e = filepath.Abs(args[1])
			case "stop-timeout":
				d.Settings.StopTimeout, e = time.ParseDuration(args[1])
			case "log-bytes":
				d.Settings.LogBytes, e = strconv.ParseInt(args[1], 10, 64)
			case "log-backups":
				d.Settings.LogBackups, e = strconv.Atoi(args[1])
			case "retain-sessions":
				d.Settings.RetainSessions, e = strconv.Atoi(args[1])
			default:
				return fmt.Errorf("unknown setting %q", args[0])
			}
			return e
		})
	}}
}

// Execute allows tests to supply cancellation without changing global signals.
func Execute(ctx context.Context, version string) error { return New(version).ExecuteContext(ctx) }
