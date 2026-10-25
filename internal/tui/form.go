package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/procly/procly/internal/config"
)

func (m *Model) beginForm(edit string) tea.Cmd {
	m.mode = "form"
	m.formKind = "command"
	m.field = 0
	m.fields = config.CommandFields()
	var err error
	m.values, err = config.CommandValues(m.doc.Aliases[edit], m.doc.Settings)
	if err != nil {
		m.mode = ""
		m.notice = err.Error()
		return nil
	}
	if edit != "" {
		m.formKind = "edit"
	}
	if m.section == 3 {
		m.formKind = "group"
		m.fields = []string{"Group name", "Command names (comma separated)"}
		m.values = []string{"", ""}
	}
	if m.section == 5 {
		m.formKind = "schedule"
		m.fields = []string{"Schedule name", "Saved command name", "Timing: every 30m or at YYYY-MM-DD HH:MM"}
		m.values = []string{"", "", "every 30m"}
	}
	if m.formKind == "edit" {
		m.field = 1
	}
	m.input.SetValue(m.values[m.field])
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
		if m.formKind == "edit" {
			m.field = max(1, m.field)
		}
		m.input.SetValue(m.values[m.field])
		return m, m.input.Focus()
	}
	if key == "enter" || key == "tab" {
		if m.field == len(m.fields) {
			if key != "enter" {
				return m, nil
			}
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
			d.Groups[name] = config.SplitNames(m.values[1])
		case "schedule":
			if _, ok := d.Schedules[name]; ok {
				return fmt.Errorf("schedule %s already exists", name)
			}
			timing := strings.TrimSpace(m.values[2])
			var at, every string
			if strings.HasPrefix(timing, "every ") {
				every = strings.TrimPrefix(timing, "every ")
			} else if strings.HasPrefix(timing, "at ") {
				at = strings.TrimPrefix(timing, "at ")
			} else {
				return fmt.Errorf("use every 30m or at YYYY-MM-DD HH:MM")
			}
			s, e := config.NewSchedule(name, strings.TrimSpace(m.values[1]), at, every, time.Now())
			if e != nil {
				return e
			}
			d.Schedules[name] = s
		default:
			if _, ok := d.Aliases[name]; ok && m.formKind != "edit" {
				return fmt.Errorf("command %s already exists", name)
			}
			if m.formKind == "edit" {
				if _, ok := d.Aliases[name]; !ok {
					return fmt.Errorf("no saved command %q", name)
				}
			}
			a, e := config.CommandFromValues(m.values)
			if e != nil {
				return e
			}
			d.Aliases[name] = a
		}
		return nil
	})
}
func (m Model) formView() string {
	if m.field < len(m.fields) {
		hint := "Enter next   Shift+Tab previous   Esc cancel"
		if m.formKind == "command" || m.formKind == "edit" {
			hint += "\n" + config.CommandHelp
		}
		return m.style.header("Save "+m.formTitle()) + fmt.Sprintf("  %d/%d\n\n", m.field+1, len(m.fields)) + m.fields[m.field] + "\n\n" + m.input.View() + "\n\n" + m.style.hint(hint)
	}
	return m.style.header("Review "+m.formTitle()) + "\n" + m.viewport.View() + "\n" + m.style.hint("Enter save   Shift+Tab edit   Esc cancel")
}
func (m Model) reviewContent() string {
	var b strings.Builder
	for i, label := range m.fields {
		fmt.Fprintf(&b, "%s\n  %s\n", label, clean(m.values[i]))
	}
	return b.String()
}

func (m Model) formTitle() string {
	if m.formKind == "edit" {
		return "command"
	}
	return m.formKind
}
