package components

import (
	"strings"

	"github.com/olivierpoupier/patch/tui"

	"charm.land/lipgloss/v2"
)

// MenuItem is a single row in a MenuList. Group headers are inserted
// automatically by MenuList between items whose Group changes.
type MenuItem struct {
	ID    string
	Label string
	Group string
}

// MenuList is a cursor-navigable list with group headers, intended for
// device-modal menus. It uses the terminal sub-theme so it visually belongs
// inside the device modal, mirroring DeviceTable's role on the main tabs.
type MenuList struct {
	theme *tui.TerminalTheme
	items []MenuItem
	rows  []menuRow // flattened render rows (headers + items)
	// cursor indexes the slice of selectable rows; clamped to len(items)-1.
	cursor int
	width  int
}

// menuRow is the flattened rendering unit: either a group header or an item.
type menuRow struct {
	header bool
	label  string
	itemIx int // index into MenuList.items, valid when !header
}

// NewMenuList creates a MenuList with the given terminal theme and items.
func NewMenuList(theme *tui.TerminalTheme, items []MenuItem) MenuList {
	m := MenuList{theme: theme}
	return m.SetItems(items)
}

// SetItems replaces the menu items and rebuilds the flattened row list.
func (m MenuList) SetItems(items []MenuItem) MenuList {
	m.items = items
	m.rows = m.rows[:0]
	prev := ""
	for i, it := range items {
		if it.Group != prev {
			m.rows = append(m.rows, menuRow{header: true, label: it.Group})
			prev = it.Group
		}
		m.rows = append(m.rows, menuRow{header: false, label: it.Label, itemIx: i})
	}
	if m.cursor >= len(items) {
		m.cursor = len(items) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	return m
}

// SetWidth stores the available render width.
func (m MenuList) SetWidth(w int) MenuList {
	m.width = w
	return m
}

// CursorUp moves the cursor up one item (skipping headers).
func (m MenuList) CursorUp() MenuList {
	if m.cursor > 0 {
		m.cursor--
	}
	return m
}

// CursorDown moves the cursor down one item.
func (m MenuList) CursorDown() MenuList {
	if m.cursor < len(m.items)-1 {
		m.cursor++
	}
	return m
}

// Cursor returns the current cursor position (item index).
func (m MenuList) Cursor() int { return m.cursor }

// Selected returns the currently selected item, or nil if the list is empty.
func (m MenuList) Selected() *MenuItem {
	if len(m.items) == 0 {
		return nil
	}
	if m.cursor < 0 || m.cursor >= len(m.items) {
		return nil
	}
	it := m.items[m.cursor]
	return &it
}

// View renders the menu as a string. The cursor row is highlighted with a
// reversed-style indicator and a bright label; group headers are dimmed.
func (m MenuList) View() string {
	if len(m.rows) == 0 {
		return m.theme.Dim.Render("  (no commands)")
	}
	var b strings.Builder
	cursorStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(m.theme.Bg).
		Background(m.theme.Accent)
	itemStyle := m.theme.Body
	groupStyle := m.theme.HeaderKey.Bold(true)

	for _, row := range m.rows {
		if row.header {
			b.WriteString("\n  ")
			b.WriteString(groupStyle.Render(strings.ToUpper(row.label)))
			b.WriteString("\n")
			continue
		}
		marker := "  "
		label := itemStyle.Render(row.label)
		if row.itemIx == m.cursor {
			marker = cursorStyle.Render(" ▸ ")
			label = cursorStyle.Render(" " + row.label + " ")
		} else {
			marker = "    "
		}
		b.WriteString(marker)
		b.WriteString(label)
		b.WriteString("\n")
	}
	return b.String()
}
