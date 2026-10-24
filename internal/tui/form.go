package tui

import (
	tea "charm.land/bubbletea/v2"
	"fmt"
	"github.com/procly/procly/internal/config"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func (m *Model) beginForm(edit string) tea.Cmd {
	m.mode = "form"
	m.formKind = "command"
	m.field = 0
	dir := m.doc.Settings.DefaultDirectory
	if dir == "" {
		dir, _ = os.Getwd()
	}
	m.fields = []string{"Name", "Command", "Mode: direct or shell", "Working directory", "Required environment names (comma separated)", "Restart: never, on-failure or always"}
	m.values = []string{"", "", "direct", dir, "", "never"}
	if edit != "" {
		a := m.doc.Aliases[edit]
		m.formKind = "edit"
		m.values = []string{a.Name, config.CommandLine(a), "direct", a.Directory, strings.Join(a.RequiredEnv, ","), a.Restart}
		if a.Shell {
			m.values[2] = "shell"
		}
		if m.values[5] == "" {
			m.values[5] = "never"
		}
	}
	if m.section == 3 {
		m.formKind = "group"
		m.fields = []string{"Group name", "Command names (comma separated)"}
		m.values = []string{"", ""}
	}
	if m.section == 5 {
		m.formKind = "schedule"
		m.fields = []string{"Schedule name", "Saved command name", "Timing: every 30m or at 2026-11-01T21:00:00+05:30"}
		m.values = []string{"", "", "every 30m"}
	}
	m.input.SetValue(m.values[0])
	return m.input.Focus()
}
func (m Model) formUpdate(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if key == "esc" {
		m.mode = ""
		m.input.Blur()
		return m, nil
	}
	if key == "shift+tab" {
		if m.field < len(m.fields) {
			m.values[m.field] = m.input.Value()
		}
		m.field = max(0, m.field-1)
		m.input.SetValue(m.values[m.field])
		return m, m.input.Focus()
	}
	if key == "enter" || key == "tab" {
		if m.field == len(m.fields) {
			m.mode = ""
			m.busy = true
			return m, m.do(func() error { return m.saveForm() }, "Saved "+m.values[0])
		}
		m.values[m.field] = m.input.Value()
		m.field++
		if m.field == len(m.fields) {
			m.input.Blur()
			m.viewport.SetContent(m.reviewContent())
			m.viewport.GotoTop()
			return m, nil
		}
		m.input.SetValue(m.values[m.field])
		return m, m.input.Focus()
	}
	if m.field < len(m.fields) {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}
	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)
	return m, cmd
}
func (m Model) saveForm() error {
	return m.controller.Store.Update(func(d *config.Document) error {
		name := strings.TrimSpace(m.values[0])
		if !config.ValidName(name) {
			return fmt.Errorf("invalid name %q", name)
		}
		switch m.formKind {
		case "group":
			if _, ok := d.Groups[name]; ok {
				return fmt.Errorf("group %s already exists", name)
			}
			d.Groups[name] = splitNames(m.values[1])
		case "schedule":
			if _, ok := d.Schedules[name]; ok {
				return fmt.Errorf("schedule %s already exists", name)
			}
			s := config.Schedule{Name: name, Alias: strings.TrimSpace(m.values[1]), Enabled: true}
			timing := strings.TrimSpace(m.values[2])
			var e error
			if strings.HasPrefix(timing, "every ") {
				s.Every, e = time.ParseDuration(strings.TrimPrefix(timing, "every "))
				s.Next = time.Now().Add(s.Every)
			} else if strings.HasPrefix(timing, "at ") {
				s.At, e = time.Parse(time.RFC3339, strings.TrimPrefix(timing, "at "))
			} else {
				return fmt.Errorf("use every 30m or at an RFC3339 date with timezone")
			}
			if e != nil {
				return e
			}
			d.Schedules[name] = s
		default:
			if _, ok := d.Aliases[name]; ok && m.formKind != "edit" {
				return fmt.Errorf("command %s already exists", name)
			}
			a := config.Alias{Name: name, Restart: strings.TrimSpace(m.values[5]), RequiredEnv: splitNames(m.values[4])}
			var e error
			a.Directory, e = filepath.Abs(m.values[3])
			if e != nil {
				return e
			}
			switch strings.TrimSpace(m.values[2]) {
			case "shell":
				a.Shell = true
				a.Executable = m.values[1]
			case "direct":
				a.Executable, a.Args, e = config.ParseCommand(m.values[1])
				if e != nil {
					return e
				}
			default:
				return fmt.Errorf("mode must be direct or shell")
			}
			if e = a.ValidateLaunch(); e != nil {
				return e
			}
			d.Aliases[name] = a
		}
		return nil
	})
}
func splitNames(s string) []string {
	var names []string
	for _, name := range strings.Split(s, ",") {
		if name = strings.TrimSpace(name); name != "" {
			names = append(names, name)
		}
	}
	return names
}
func (m Model) formView() string {
	if m.field < len(m.fields) {
		return m.style.header("Save "+m.formKind) + fmt.Sprintf("  %d/%d\n\n", m.field+1, len(m.fields)) + m.fields[m.field] + "\n\n" + m.input.View() + "\n\n" + m.style.hint("Enter next   Shift+Tab previous   Esc cancel\nChoose shell mode for npm, pipes and &&.\nEnvironment values are never stored.")
	}
	return m.style.header("Review "+m.formKind) + "\n" + m.viewport.View() + "\n" + m.style.hint("Enter save   Shift+Tab edit   Esc cancel")
}
func (m Model) reviewContent() string {
	var b strings.Builder
	for i, label := range m.fields {
		fmt.Fprintf(&b, "%s\n  %s\n", label, clean(m.values[i]))
	}
	return b.String()
}
