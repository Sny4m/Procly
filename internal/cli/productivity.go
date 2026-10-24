package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/procly/procly/internal/config"
	"github.com/procly/procly/internal/session"
	"github.com/procly/procly/internal/storage"
	"github.com/procly/procly/internal/tui"
	"github.com/spf13/cobra"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"
)

func remoteCommands(store storeFunc) []*cobra.Command {
	var commands []*cobra.Command
	for _, action := range []string{"ps", "stop", "restart"} {
		action := action
		args := cobra.ExactArgs(1)
		use := action + " NAME"
		if action == "ps" {
			args = cobra.NoArgs
			use = action
		}
		commands = append(commands, &cobra.Command{Use: use, Short: map[string]string{"ps": "Show processes owned by the active session", "stop": "Stop a command in the active session", "restart": "Restart a command in the active session"}[action], Args: args, RunE: func(c *cobra.Command, args []string) error {
			s, e := store()
			if e != nil {
				return e
			}
			req := session.Request{Action: action}
			if len(args) > 0 {
				req.Name = args[0]
			}
			r, e := session.Call(c.Context(), s, req)
			if e != nil {
				return e
			}
			if action == "ps" {
				w := tabwriter.NewWriter(c.OutOrStdout(), 0, 4, 3, ' ', 0)
				fmt.Fprintln(w, "NAME\tSTATE\tPID\tSTARTED")
				for _, p := range r.Records {
					fmt.Fprintf(w, "%s\t%s\t%d\t%s\n", safeText(p.Alias), p.State, p.PID, p.Started.Format(time.RFC3339))
				}
				return w.Flush()
			}
			fmt.Fprintln(c.OutOrStdout(), action+":", req.Name)
			return nil
		}})
	}
	return commands
}
func followLogs(c *cobra.Command, s *storage.Store, name, stream string) error {
	var sequence uint64
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		r, e := session.Call(c.Context(), s, session.Request{Action: "logs", Name: name, Stream: stream, After: sequence})
		if e != nil {
			return e
		}
		for _, event := range r.Events {
			if _, e = fmt.Fprintf(c.OutOrStdout(), "%s %s %s", event.Time.Format("15:04:05"), event.Stream, event.Text); e != nil {
				return e
			}
			sequence = event.Sequence
		}
		select {
		case <-c.Context().Done():
			return nil
		case <-ticker.C:
		}
	}
}
func groupCommand(store storeFunc) *cobra.Command {
	root := &cobra.Command{Use: "group", Short: "Save and run command workspaces"}
	root.AddCommand(&cobra.Command{Use: "create NAME", Args: cobra.ExactArgs(1), Short: "Create a workspace", RunE: func(c *cobra.Command, args []string) error {
		s, e := store()
		if e != nil {
			return e
		}
		return s.Update(func(d *config.Document) error {
			if _, ok := d.Groups[args[0]]; ok {
				return errors.New("group already exists")
			}
			d.Groups[args[0]] = []string{}
			return nil
		})
	}})
	root.AddCommand(&cobra.Command{Use: "add GROUP COMMAND...", Args: cobra.MinimumNArgs(2), Short: "Add saved commands to a workspace", RunE: func(c *cobra.Command, args []string) error {
		s, e := store()
		if e != nil {
			return e
		}
		return s.Update(func(d *config.Document) error {
			members, ok := d.Groups[args[0]]
			if !ok {
				return errors.New("group does not exist")
			}
			d.Groups[args[0]] = append(members, args[1:]...)
			return nil
		})
	}})
	root.AddCommand(&cobra.Command{Use: "ls", Aliases: []string{"list"}, Args: cobra.NoArgs, Short: "List workspaces", RunE: func(c *cobra.Command, _ []string) error {
		s, e := store()
		if e != nil {
			return e
		}
		d, e := s.Load()
		if e != nil {
			return e
		}
		return json.NewEncoder(c.OutOrStdout()).Encode(d.Groups)
	}})
	root.AddCommand(&cobra.Command{Use: "rm NAME", Args: cobra.ExactArgs(1), Short: "Remove a group definition", RunE: func(c *cobra.Command, args []string) error {
		s, e := store()
		if e != nil {
			return e
		}
		return s.Update(func(d *config.Document) error {
			if _, ok := d.Groups[args[0]]; !ok {
				return errors.New("group does not exist")
			}
			delete(d.Groups, args[0])
			return nil
		})
	}})
	for _, action := range []string{"run", "stop"} {
		action := action
		root.AddCommand(&cobra.Command{Use: action + " NAME", Args: cobra.ExactArgs(1), Short: action + " a workspace in the active session", RunE: func(c *cobra.Command, args []string) error {
			s, e := store()
			if e != nil {
				return e
			}
			op := "group-start"
			if action == "stop" {
				op = "group-stop"
			}
			r, e := session.Call(c.Context(), s, session.Request{Action: op, Name: args[0]})
			if e != nil {
				if action == "run" {
					return tui.RunGroup(c.Context(), s, "", args[0])
				}
				return e
			}
			failed := false
			for member, result := range r.Members {
				fmt.Fprintf(c.OutOrStdout(), "%s: %s\n", member, result)
				if result != "started" && result != "stopped" {
					failed = true
				}
			}
			if failed {
				return errors.New("some group members failed; see results above")
			}
			return nil
		}})
	}
	return root
}
func scheduleCommand(store storeFunc) *cobra.Command {
	root := &cobra.Command{Use: "schedule", Short: "Schedule commands while the dashboard is open"}
	var at, every, alias string
	add := &cobra.Command{Use: "add NAME", Args: cobra.ExactArgs(1), Short: "Add a one-time or interval schedule", RunE: func(c *cobra.Command, args []string) error {
		s, e := store()
		if e != nil {
			return e
		}
		sc := config.Schedule{Name: args[0], Alias: alias, Enabled: true}
		if at != "" {
			sc.At, e = time.Parse(time.RFC3339, at)
			if e != nil {
				sc.At, e = time.ParseInLocation("2006-01-02 15:04", at, time.Local)
			}
			if e != nil {
				return fmt.Errorf("use RFC3339 with timezone or local YYYY-MM-DD HH:MM: %w", e)
			}
			if !sc.At.After(time.Now()) {
				return errors.New("scheduled time must be in the future")
			}
		}
		if every != "" {
			sc.Every, e = time.ParseDuration(every)
			if e != nil {
				return e
			}
			sc.Next = time.Now().Add(sc.Every)
		}
		e = s.Update(func(d *config.Document) error {
			if _, ok := d.Schedules[sc.Name]; ok {
				return errors.New("schedule already exists")
			}
			d.Schedules[sc.Name] = sc
			return nil
		})
		if e == nil {
			fmt.Fprintln(c.OutOrStdout(), "Schedule saved. Runs only while the Procly dashboard is open.")
		}
		return e
	}}
	add.Flags().StringVar(&at, "at", "", "One-time local date or RFC3339 timestamp")
	add.Flags().StringVar(&every, "every", "", "Interval, for example 30m")
	add.Flags().StringVar(&alias, "run", "", "Saved command name")
	root.AddCommand(add)
	for _, action := range []string{"ls", "cancel", "enable", "disable", "history"} {
		action := action
		args := cobra.ExactArgs(1)
		use := action + " NAME"
		if action == "ls" {
			args = cobra.NoArgs
			use = action
		}
		root.AddCommand(&cobra.Command{Use: use, Args: args, Short: action + " schedule definitions", RunE: func(c *cobra.Command, args []string) error {
			s, e := store()
			if e != nil {
				return e
			}
			if action == "ls" || action == "history" {
				d, e := s.Load()
				if e != nil {
					return e
				}
				enc := json.NewEncoder(c.OutOrStdout())
				enc.SetIndent("", "  ")
				if action == "history" {
					sc, ok := d.Schedules[args[0]]
					if !ok {
						return errors.New("schedule not found")
					}
					return enc.Encode(sc.History)
				}
				fmt.Fprintln(c.OutOrStdout(), "Schedules execute only while the dashboard is open.")
				return enc.Encode(d.Schedules)
			}
			return s.Update(func(d *config.Document) error {
				sc, ok := d.Schedules[args[0]]
				if !ok {
					return errors.New("schedule not found")
				}
				if action == "cancel" {
					delete(d.Schedules, args[0])
					return nil
				}
				sc.Enabled = action == "enable"
				if sc.Enabled && sc.Every > 0 {
					sc.Next = time.Now().Add(sc.Every)
				}
				d.Schedules[args[0]] = sc
				return nil
			})
		}})
	}
	return root
}
func transferCommand(store storeFunc, importing bool) *cobra.Command {
	verb := "export"
	if importing {
		verb = "import"
	}
	return &cobra.Command{Use: verb + " FILE", Args: cobra.ExactArgs(1), Short: verb + " command, group, schedule and setting definitions", RunE: func(c *cobra.Command, args []string) error {
		s, e := store()
		if e != nil {
			return e
		}
		if !importing {
			d, e := s.Load()
			if e != nil {
				return e
			}
			f, e := os.OpenFile(args[0], os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if e != nil {
				return e
			}
			defer f.Close()
			enc := json.NewEncoder(f)
			enc.SetIndent("", "  ")
			return enc.Encode(d)
		}
		f, e := os.Open(args[0])
		if e != nil {
			return e
		}
		defer f.Close()
		var incoming config.Document
		dec := json.NewDecoder(io.LimitReader(f, 4*1024*1024))
		dec.DisallowUnknownFields()
		if e = dec.Decode(&incoming); e != nil {
			return e
		}
		incoming.Normalize()
		if e = incoming.Validate(); e != nil {
			return e
		}
		return s.Update(func(d *config.Document) error {
			for name, a := range incoming.Aliases {
				if _, exists := d.Aliases[name]; exists {
					return fmt.Errorf("command %s already exists; import does not overwrite", name)
				}
				d.Aliases[name] = a
			}
			for name, g := range incoming.Groups {
				if _, exists := d.Groups[name]; exists {
					return fmt.Errorf("group %s already exists", name)
				}
				d.Groups[name] = g
			}
			for name, sc := range incoming.Schedules {
				if _, exists := d.Schedules[name]; exists {
					return fmt.Errorf("schedule %s already exists", name)
				}
				sc.Enabled = false
				d.Schedules[name] = sc
			}
			return nil
		})
	}}
}

var _ = strings.TrimSpace
