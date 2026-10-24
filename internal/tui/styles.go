package tui

import (
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"strings"
)

const Logo = "                          _\n  _ __  _ __ ___   ___   | |_   _\n | '_ \\| '__/ _ \\ / __|  | | | | |\n | |_) | | | (_) | (__   | | |_| |\n | .__/|_|  \\___/ \\___|  |_|\\__, |\n |_|                        |___/"

type styles struct {
	accent, muted, text, good, bad, selected lipgloss.Style
	plain                                    bool
}

func palette(light, plain bool) styles {
	fg, muted, accent, green, red, bg := "#E8E3DA", "#9D968C", "#D97757", "#91B98B", "#E17D72", "#383029"
	if light {
		fg, muted, accent, green, red, bg = "#302B26", "#777066", "#B95E3D", "#397044", "#B33F35", "#F2E7DE"
	}
	s := styles{plain: plain}
	if plain {
		return s
	}
	s.text = lipgloss.NewStyle().Foreground(lipgloss.Color(fg))
	s.muted = lipgloss.NewStyle().Foreground(lipgloss.Color(muted))
	s.accent = lipgloss.NewStyle().Foreground(lipgloss.Color(accent))
	s.good = lipgloss.NewStyle().Foreground(lipgloss.Color(green))
	s.bad = lipgloss.NewStyle().Foreground(lipgloss.Color(red))
	s.selected = lipgloss.NewStyle().Foreground(lipgloss.Color(fg)).Background(lipgloss.Color(bg)).Bold(true)
	return s
}
func clean(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, ansi.Strip(s))
}
func clip(s string, width int) string {
	if width < 1 {
		return ""
	}
	return ansi.Truncate(s, width, "~")
}
func (s styles) header(title string) string { return s.accent.Bold(true).Render(title) }
func (s styles) hint(keys string) string    { return s.muted.Render(keys) }
func (s styles) status(state string) string {
	switch state {
	case "running":
		return s.good.Render(state)
	case "failed":
		return s.bad.Render(state)
	default:
		return s.muted.Render(state)
	}
}
