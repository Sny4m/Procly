package tui

import (
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/charmbracelet/x/ansi"
	"github.com/procly/procly/internal/config"
	"github.com/procly/procly/internal/process"
	"github.com/procly/procly/internal/session"
	"github.com/procly/procly/internal/storage"
	"os"
	"sort"
	"strings"
	"time"
)

var sections = []string{"Overview", "Processes", "Commands", "Groups", "Logs", "Schedules", "Settings"}

type tickMsg time.Time
type refreshMsg struct {
	doc     config.Document
	records []process.Record
	err     error
}
type actionMsg struct {
	text string
	err  error
}
type quitMsg struct{ err error }
type Model struct {
	controller                                             *session.Controller
	doc                                                    config.Document
	records                                                []process.Record
	version                                                string
	width, height, section, selected                       int
	light                                                  bool
	mode, query, logAlias, stream, notice, dialog, pending string
	busy, follow, searching                                bool
	input                                                  textinput.Model
	viewport                                               viewport.Model
	formKind                                               string
	fields, values                                         []string
	field                                                  int
	style                                                  styles
	err                                                    error
	logSequence                                            uint64
	logRendered                                            string
}

func New(c *session.Controller, d config.Document, version string) Model {
	input := textinput.New()
	input.CharLimit = 8192
	input.SetVirtualCursor(true)
	m := Model{controller: c, doc: d, version: version, width: 80, height: 24, follow: true, input: input, viewport: viewport.New(viewport.WithWidth(76), viewport.WithHeight(12))}
	m.viewport.SoftWrap = true
	m.restyle()
	return m
}
func (m *Model) restyle() {
	plain := m.doc.Settings.Color == "never" || os.Getenv("NO_COLOR") != ""
	light := m.light
	if m.doc.Settings.Theme == "light" {
		light = true
	}
	if m.doc.Settings.Theme == "dark" {
		light = false
	}
	m.style = palette(light, plain)
	m.input.SetStyles(textinput.DefaultStyles(!light))
}
func tick() tea.Cmd { return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) }) }
func (m Model) refresh() tea.Cmd {
	return func() tea.Msg {
		d, e := m.controller.Store.Load()
		return refreshMsg{d, m.controller.Manager.Snapshots(), e}
	}
}
func (m Model) Init() tea.Cmd { return tea.Batch(tea.RequestBackgroundColor, m.refresh(), tick()) }
func (m Model) rows() []string {
	var rows []string
	switch m.section {
	case 3:
		for name := range m.doc.Groups {
			rows = append(rows, name)
		}
	case 5:
		for name := range m.doc.Schedules {
			rows = append(rows, name)
		}
	case 6:
		return []string{"Theme", "Color", "Stop timeout", "Log backups", "Retained sessions"}
	default:
		for name := range m.doc.Aliases {
			if m.query == "" || strings.Contains(strings.ToLower(name+" "+m.doc.Aliases[name].Executable), strings.ToLower(m.query)) {
				if m.section != 1 || m.latest(name).ID != "" {
					rows = append(rows, name)
				}
			}
		}
	}
	sort.Strings(rows)
	return rows
}
func (m Model) chosen() string {
	rows := m.rows()
	if len(rows) == 0 {
		return ""
	}
	return rows[min(m.selected, len(rows)-1)]
}
func (m Model) latest(name string) process.Record {
	var latest process.Record
	for _, r := range m.records {
		if r.Alias == name && r.Started.After(latest.Started) {
			latest = r
		}
	}
	return latest
}
func (m Model) running() int {
	n := 0
	for _, r := range m.records {
		if r.State == process.Running || r.State == process.Stopping {
			n++
		}
	}
	return n
}
func (m Model) do(fn func() error, text string) tea.Cmd {
	return func() tea.Msg { return actionMsg{text, fn()} }
}
func (m *Model) closeCommand() tea.Cmd {
	m.busy = true
	m.notice = "Stopping owned processes..."
	return func() tea.Msg { return quitMsg{m.controller.Close()} }
}
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.input.SetWidth(max(8, msg.Width-8))
		m.viewport.SetWidth(max(1, msg.Width-4))
		m.viewport.SetHeight(max(1, msg.Height-10))
		return m, nil
	case tea.BackgroundColorMsg:
		m.light = !msg.IsDark()
		m.restyle()
		return m, nil
	case tickMsg:
		if m.mode == "logs" && m.follow {
			m.updateLogs()
		}
		return m, tea.Batch(tick(), m.refresh())
	case refreshMsg:
		if msg.err != nil {
			m.notice = msg.err.Error()
		} else {
			m.doc = msg.doc
			m.records = msg.records
			m.restyle()
		}
		m.selected = min(m.selected, max(0, len(m.rows())-1))
		return m, nil
	case actionMsg:
		m.busy = false
		if msg.err != nil {
			m.notice = "Error: " + msg.err.Error()
			m.viewport.SetContent(clean(m.notice))
			m.mode = "message"
		} else {
			m.notice = msg.text
		}
		return m, m.refresh()
	case quitMsg:
		m.err = msg.err
		return m, tea.Quit
	case tea.KeyPressMsg:
		key := msg.String()
		if key == "ctrl+c" {
			return m, m.closeCommand()
		}
		if m.busy {
			return m, nil
		}
		if m.dialog != "" {
			switch key {
			case "esc", "n":
				m.dialog = ""
			case "y", "enter":
				action := m.pending
				m.dialog = ""
				m.busy = true
				switch action {
				case "quit":
					return m, m.closeCommand()
				case "restart":
					name := m.chosen()
					return m, m.do(func() error { return m.controller.Restart(name) }, "Restarted "+name)
				case "stop":
					name := m.chosen()
					return m, m.do(func() error { return m.controller.Stop(name) }, "Stopped "+name)
				case "group-stop":
					name := m.chosen()
					return m, func() tea.Msg { return actionMsg{formatMembers(m.controller.Group(name, true)), nil} }
				case "remove":
					name := m.chosen()
					return m, m.do(func() error {
						return m.controller.Store.Update(func(d *config.Document) error { delete(d.Aliases, name); return nil })
					}, "Removed "+name)
				}
			}
			return m, nil
		}
		if m.searching {
			if key == "enter" || key == "esc" {
				m.searching = false
				m.query = m.input.Value()
				m.input.Blur()
				if m.mode == "logs" {
					m.resetLogs()
				}
				return m, nil
			}
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			m.query = m.input.Value()
			m.selected = 0
			return m, cmd
		}
		if m.mode == "form" {
			return m.formUpdate(msg)
		}
		if m.mode != "" {
			switch key {
			case "esc":
				m.mode = ""
				m.query = ""
				return m, nil
			case "q":
				m.mode = ""
				return m, nil
			case "/":
				m.searching = true
				m.input.SetValue(m.query)
				return m, m.input.Focus()
			case "f":
				if m.mode == "logs" {
					m.follow = !m.follow
					m.updateLogs()
					return m, nil
				}
			case "t":
				if m.mode == "logs" {
					switch m.stream {
					case "":
						m.stream = "stdout"
					case "stdout":
						m.stream = "stderr"
					default:
						m.stream = ""
					}
					m.resetLogs()
					return m, nil
				}
			}
			if m.mode == "logs" && (key == "up" || key == "pgup" || key == "k") {
				m.follow = false
			}
			var cmd tea.Cmd
			m.viewport, cmd = m.viewport.Update(msg)
			return m, cmd
		}
		switch key {
		case "q":
			if m.running() == 0 {
				return m, m.closeCommand()
			}
			m.dialog = "Stop all running processes and quit?"
			m.pending = "quit"
		case "tab":
			m.section = (m.section + 1) % len(sections)
			m.selected = 0
			m.query = ""
		case "shift+tab":
			m.section = (m.section + len(sections) - 1) % len(sections)
			m.selected = 0
			m.query = ""
		case "up", "k":
			m.selected = max(0, m.selected-1)
		case "down", "j":
			m.selected = min(max(0, len(m.rows())-1), m.selected+1)
		case "/":
			m.searching = true
			m.input.SetValue(m.query)
			return m, m.input.Focus()
		case "?":
			m.mode = "help"
			m.viewport.SetContent("KEYBOARD\n\nj/k or arrows   Move selection\nEnter           Details or open section\na               Add command, group or schedule\ne               Edit selected command\nr               Run; confirm restart when active\ns               Stop selected process\nx               Remove saved command\nl               Open live logs\n/               Search commands or logs\nTab             Next section\nf               Follow / pause logs\nt               stdout / stderr / all logs\nSpace           Enable / disable schedule\nq               Quit (confirm while running)\nEsc             Back / cancel\n\nSchedules execute only while this dashboard is open.\nSettings changes apply to newly opened sessions.\nUse procly help for scripting and import/export.")
		case "a":
			return m, m.beginForm("")
		case "e":
			if _, ok := m.doc.Aliases[m.chosen()]; ok {
				return m, m.beginForm(m.chosen())
			}
		case "l":
			m.logAlias = m.chosen()
			m.mode = "logs"
			m.follow = true
			m.resetLogs()
		case "r":
			name := m.chosen()
			if name == "" {
				break
			}
			if m.section == 3 {
				m.busy = true
				return m, func() tea.Msg { return actionMsg{formatMembers(m.controller.Group(name, false)), nil} }
			}
			r := m.latest(name)
			if r.State == process.Running {
				m.dialog = "Restart " + name + "?"
				m.pending = "restart"
			} else {
				m.busy = true
				return m, m.do(func() error { _, e := m.controller.Start(name, process.Output{}); return e }, "Started "+name)
			}
		case "s":
			name := m.chosen()
			if name == "" {
				break
			}
			if m.section == 3 {
				m.dialog = "Stop all running members of " + name + "?"
				m.pending = "group-stop"
				return m, nil
			}
			if m.latest(name).State == process.Running {
				m.dialog = "Stop " + name + "?"
				m.pending = "stop"
			}
		case "x":
			if _, ok := m.doc.Aliases[m.chosen()]; ok {
				m.dialog = "Remove saved command " + m.chosen() + "?"
				m.pending = "remove"
			}
		case "space":
			if m.section == 5 {
				name := m.chosen()
				m.busy = true
				return m, m.do(func() error {
					return m.controller.Store.Update(func(d *config.Document) error {
						s := d.Schedules[name]
						s.Enabled = !s.Enabled
						if s.Every > 0 {
							s.Next = time.Now().Add(s.Every)
						}
						d.Schedules[name] = s
						return nil
					})
				}, "Schedule updated")
			}
		case "enter":
			name := m.chosen()
			if name == "" {
				break
			}
			if m.section == 4 {
				m.logAlias = name
				m.mode = "logs"
				m.follow = true
				m.resetLogs()
			} else if m.section == 6 {
				return m, m.changeSetting()
			} else {
				m.mode = "details"
				var data any
				if m.section == 3 {
					data = m.doc.Groups[name]
				} else if m.section == 5 {
					data = m.doc.Schedules[name]
				} else {
					data = struct {
						Command config.Alias
						Session process.Record
					}{m.doc.Aliases[name], m.latest(name)}
				}
				b, _ := json.MarshalIndent(data, "", "  ")
				m.viewport.SetContent(clean(string(b)))
				m.viewport.GotoTop()
			}
		}
	}
	return m, nil
}
func formatMembers(members map[string]string) string {
	var names []string
	for name := range members {
		names = append(names, name)
	}
	sort.Strings(names)
	var lines []string
	for _, name := range names {
		lines = append(lines, name+": "+members[name])
	}
	return strings.Join(lines, "; ")
}
func (m *Model) updateLogs() {
	events := m.controller.Logs.Read(m.logAlias, m.stream, m.query, m.logSequence)
	if len(events) == 0 && m.logRendered != "" {
		if m.follow {
			m.viewport.GotoBottom()
		}
		return
	}
	var b strings.Builder
	b.WriteString(m.logRendered)
	for _, e := range events {
		m.logSequence = e.Sequence
		fmt.Fprintf(&b, "%s %-6s %s", e.Time.Format("15:04:05"), e.Stream, clean(e.Text))
		if !strings.HasSuffix(e.Text, "\n") {
			b.WriteByte('\n')
		}
	}
	if b.Len() == 0 {
		b.WriteString("No matching output in this session.\nFor previous runs: procly logs " + m.logAlias)
	}
	m.logRendered = b.String()
	if len(m.logRendered) > 256*1024 {
		m.logRendered = m.logRendered[len(m.logRendered)-256*1024:]
		if newline := strings.IndexByte(m.logRendered, '\n'); newline >= 0 {
			m.logRendered = m.logRendered[newline+1:]
		}
	}
	m.viewport.SetContent(m.logRendered)
	if m.follow {
		m.viewport.GotoBottom()
	}
}
func (m *Model) resetLogs() { m.logSequence = 0; m.logRendered = ""; m.updateLogs() }
func (m Model) changeSetting() tea.Cmd {
	name := m.chosen()
	return m.do(func() error {
		return m.controller.Store.Update(func(d *config.Document) error {
			switch name {
			case "Theme":
				switch d.Settings.Theme {
				case "dark":
					d.Settings.Theme = "light"
				case "light":
					d.Settings.Theme = "auto"
				default:
					d.Settings.Theme = "dark"
				}
			case "Color":
				if d.Settings.Color == "never" {
					d.Settings.Color = "auto"
				} else {
					d.Settings.Color = "never"
				}
			case "Stop timeout":
				d.Settings.StopTimeout += time.Second
				if d.Settings.StopTimeout > 10*time.Second {
					d.Settings.StopTimeout = time.Second
				}
			case "Log backups":
				d.Settings.LogBackups = (d.Settings.LogBackups + 1) % 6
			case "Retained sessions":
				d.Settings.RetainSessions += 25
				if d.Settings.RetainSessions > 200 {
					d.Settings.RetainSessions = 25
				}
			}
			return nil
		})
	}, "Setting saved; process settings apply next session")
}

func (m Model) View() tea.View {
	w := max(1, m.width-4)
	s := m.style
	var b strings.Builder
	if m.width >= 72 && m.height >= 28 && m.mode == "" {
		b.WriteString(s.accent.Render(Logo) + "\n")
	} else {
		b.WriteString(s.header("procly_") + "  " + s.hint(m.version) + "\n")
	}
	b.WriteString(s.hint("Run it. Track it. Control it.") + "\n\n")
	if m.dialog != "" {
		b.WriteString(s.header(m.dialog) + "\n\n" + s.hint("Enter/y  Confirm     Esc/n  Cancel"))
	} else if m.mode == "form" {
		b.WriteString(m.formView())
	} else if m.mode != "" {
		title := m.mode
		if title == "logs" {
			title = "Logs / " + m.logAlias + " / " + m.stream
			if m.follow {
				title += " / following"
			} else {
				title += " / paused"
			}
		}
		b.WriteString(s.header(title) + "\n" + m.viewport.View() + "\n" + s.hint("Esc back   / search   f follow   t stream"))
	} else {
		if m.width >= 90 {
			for i, name := range sections {
				if i == m.section {
					b.WriteString(s.accent.Bold(true).Render("[" + name + "] "))
				} else {
					b.WriteString(s.hint(name + "  "))
				}
			}
		} else {
			fmt.Fprintf(&b, "%s  %s", s.header(sections[m.section]), s.hint(fmt.Sprintf("%d/%d  Tab to switch", m.section+1, len(sections))))
		}
		b.WriteString("\n\n")
		fmt.Fprintf(&b, "%s   %s   %s\n\n", s.good.Render(fmt.Sprintf("%d running", m.running())), s.hint(fmt.Sprintf("%d completed", len(m.records)-m.running())), s.hint(fmt.Sprintf("%d saved", len(m.doc.Aliases))))
		if m.section == 5 {
			b.WriteString(s.hint("Schedules run only while Procly is open.") + "\n")
		}
		rows := m.rows()
		if len(rows) == 0 {
			if m.query != "" {
				b.WriteString("No matching commands. Press / to search again.\n")
			} else {
				b.WriteString("No entries yet.\n\nSave the commands you use every day\nand manage them all from here.\n\n" + s.accent.Render("[a] Add your first entry"))
			}
		} else {
			limit := max(1, m.height-15)
			if m.width >= 72 && m.height >= 28 {
				limit -= 6
			}
			start := max(0, m.selected-limit+1)
			end := min(len(rows), start+limit)
			for i := start; i < end; i++ {
				name := rows[i]
				line := name
				switch m.section {
				case 3:
					line += "  " + strings.Join(m.doc.Groups[name], ", ")
				case 5:
					sc := m.doc.Schedules[name]
					state := "disabled"
					if sc.Enabled {
						state = "enabled"
					}
					line += "  " + state + "  " + sc.Alias
				case 6:
					values := map[string]string{"Theme": m.doc.Settings.Theme, "Color": m.doc.Settings.Color, "Stop timeout": m.doc.Settings.StopTimeout.String(), "Log backups": fmt.Sprint(m.doc.Settings.LogBackups), "Retained sessions": fmt.Sprint(m.doc.Settings.RetainSessions)}
					line = fmt.Sprintf("%-20s %s", name, values[name])
				default:
					r := m.latest(name)
					state := string(r.State)
					if state == "" {
						state = "stopped"
					}
					if m.width >= 65 {
						uptime := "-"
						if r.State == process.Running {
							uptime = time.Since(r.Started).Truncate(time.Second).String()
						}
						line = fmt.Sprintf("%-22s %-9s %8s", name, state, uptime)
						if m.width >= 100 {
							if r.MemoryBytes > 0 {
								line += fmt.Sprintf("  %6.1f MB", float64(r.MemoryBytes)/(1024*1024))
							} else {
								line += "         -"
							}
						}
						if m.width > 100 {
							line += "  " + m.doc.Aliases[name].Executable
						}
					} else {
						line = name + "  " + state
					}
				}
				prefix := "  "
				if i == m.selected {
					prefix = "> "
				}
				line = clip(prefix+clean(line), w)
				if i == m.selected {
					line = s.selected.Render(line)
				}
				b.WriteString(line + "\n")
			}
			if end < len(rows) || start > 0 {
				b.WriteString(s.hint(fmt.Sprintf("%d-%d of %d", start+1, end, len(rows))) + "\n")
			}
		}
		b.WriteString("\n" + s.hint("a add   r run   s stop   l logs   / search   ? help   q quit"))
	}
	if m.searching {
		b.WriteString("\nSearch: " + m.input.View())
	}
	if m.notice != "" {
		b.WriteString("\n" + s.accent.Render(clip(clean(m.notice), w)))
	}
	lines := strings.Split(b.String(), "\n")
	for i := range lines {
		lines[i] = "  " + clip(lines[i], w)
	}
	if len(lines) > m.height {
		lines = lines[:max(1, m.height)]
	}
	content := strings.Join(lines, "\n")
	if s.plain {
		content = ansi.Strip(content)
	}
	v := tea.NewView(content)
	v.AltScreen = true
	return v
}

func Run(ctx context.Context, store *storage.Store, version string) (result error) {
	return RunGroup(ctx, store, version, "")
}
func RunGroup(ctx context.Context, store *storage.Store, version, group string) (result error) {
	c, e := session.Open(store)
	if e != nil {
		return e
	}
	defer func() { result = errors.Join(result, c.Close()) }()
	stop, e := session.Serve(c)
	if e != nil {
		return e
	}
	defer stop()
	c.StartScheduler()
	d, e := store.Load()
	if e != nil {
		return e
	}
	model := New(c, d, version)
	if group != "" {
		model.notice = formatMembers(c.Group(group, false))
	}
	final, e := tea.NewProgram(model, tea.WithContext(ctx)).Run()
	if e != nil && !errors.Is(e, tea.ErrProgramKilled) {
		return e
	}
	if final != nil {
		return final.(Model).err
	}
	return nil
}
