package flipper

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/olivierpoupier/patch/tui"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// gpioPins lists the stable, non-debug pins exposed on the Flipper Zero
// external header that the firmware's `gpio` CLI accepts (case-sensitive,
// uppercase chip names). Debug pins (PA13/PA14/PB6/PB7/PB8/PB9/PB14) are
// omitted: the firmware prompts for y/n confirmation on those because they
// can damage hardware, and we don't want to ship that dialog flow until the
// MVP has been validated on real hardware.
var gpioPins = []string{
	"PA7", "PA6",
	"PA4", "PB3",
	"PB2", "PC3",
	"PC1", "PC0",
}

const gpioGridCols = 2

// gpioReadIDPrefix is prepended to runner command IDs for GPIO reads, so
// handleCommandResult can route the response back into pinState instead of
// the detail pane.
const gpioReadIDPrefix = "gpio:read:"

// pinState tracks the latest known state for a single GPIO pin. Reading is
// asynchronous: pressing R sets Reading=true; the commandResult later fills
// LastValue or LastErr and clears Reading.
type pinState struct {
	Reading   bool
	HasValue  bool
	LastValue string // "0" / "1" / firmware text on parse miss
	LastErr   string
	ReadAt    time.Time
}

// gpioModel holds the GPIO sub-view state. cursor is an index into gpioPins.
type gpioModel struct {
	cursor int
	pins   map[string]*pinState
}

func newGPIOModel() gpioModel {
	pins := make(map[string]*pinState, len(gpioPins))
	for _, name := range gpioPins {
		pins[name] = &pinState{}
	}
	return gpioModel{pins: pins}
}

func (g *gpioModel) selectedPin() string {
	if g.cursor < 0 || g.cursor >= len(gpioPins) {
		return ""
	}
	return gpioPins[g.cursor]
}

// move shifts the cursor by (dx, dy) in the 2-column grid.
func (g *gpioModel) move(dx, dy int) {
	row, col := g.cursor/gpioGridCols, g.cursor%gpioGridCols
	col += dx
	row += dy
	if col < 0 {
		col = 0
	}
	if col >= gpioGridCols {
		col = gpioGridCols - 1
	}
	if row < 0 {
		row = 0
	}
	maxRow := (len(gpioPins) - 1) / gpioGridCols
	if row > maxRow {
		row = maxRow
	}
	idx := row*gpioGridCols + col
	if idx >= len(gpioPins) {
		// Last row may be partial — clamp to the last existing pin.
		idx = len(gpioPins) - 1
	}
	g.cursor = idx
}

// gpioReadValueRe extracts a 0/1 value from a `gpio read` response. We accept
// either a bare digit on its own line or a "Pin <NAME> = <0|1>" / "<NAME> = 0"
// shape. The pattern is liberal because we haven't verified the exact
// firmware string format on every Flipper firmware fork.
var gpioReadValueRe = regexp.MustCompile(`(?:^|\W)([01])(?:\W|$)`)

// parseGPIOReadResponse extracts "0" or "1" from a Flipper `gpio read`
// response, or "" if it couldn't be parsed.
func parseGPIOReadResponse(raw []byte) string {
	for _, ln := range trimEchoAndPrompt(rawLines(raw)) {
		clean := strings.TrimSpace(stripSGR(ln))
		if clean == "" {
			continue
		}
		if clean == "0" || clean == "1" {
			return clean
		}
		if m := gpioReadValueRe.FindStringSubmatch(clean); m != nil {
			return m[1]
		}
	}
	return ""
}

// gpioReadCmdID returns the runner ID used for a GPIO read on the given pin.
func gpioReadCmdID(pin string) string { return gpioReadIDPrefix + pin }

// pinFromGPIOReadID is the inverse of gpioReadCmdID.
func pinFromGPIOReadID(id string) string {
	if !strings.HasPrefix(id, gpioReadIDPrefix) {
		return ""
	}
	return strings.TrimPrefix(id, gpioReadIDPrefix)
}

// startGPIORead arms the runner for a `gpio read <pin>` and returns the
// tea.Cmd batch to issue both the timeout tick and the serial send.
func (m *Model) startGPIORead(pin string) tea.Cmd {
	if !m.session.Active() || m.disconnected {
		return nil
	}
	if m.runner.busy() {
		return nil
	}
	if m.gpio.pins[pin] == nil {
		m.gpio.pins[pin] = &pinState{}
	}
	m.gpio.pins[pin].Reading = true
	m.gpio.pins[pin].LastErr = ""
	timeoutCmd := m.runner.start(gpioReadCmdID(pin))
	sendCmd := m.session.Send([]byte("gpio read " + pin + "\r\n"))
	return tea.Batch(timeoutCmd, sendCmd)
}

// applyGPIOReadResult populates the pin state from a runner result. Returns
// true iff the result was consumed (so the caller knows not to fall through
// to the detail pane).
func (m *Model) applyGPIOReadResult(msg commandResultMsg) bool {
	pin := pinFromGPIOReadID(msg.ID)
	if pin == "" {
		return false
	}
	if m.gpio.pins[pin] == nil {
		m.gpio.pins[pin] = &pinState{}
	}
	st := m.gpio.pins[pin]
	st.Reading = false
	st.ReadAt = time.Now()
	if msg.Err != nil {
		st.LastErr = msg.Err.Error()
		return true
	}
	val := parseGPIOReadResponse(msg.Raw)
	if val == "" {
		st.LastErr = "couldn't parse value from response"
		return true
	}
	st.LastValue = val
	st.HasValue = true
	st.LastErr = ""
	return true
}

// renderGPIO draws the pin grid centered horizontally. Each cell shows pin
// name + current value (or "—" / "…" / "err"). The cursor cell is highlighted.
func (m *Model) renderGPIO(width int) string {
	t := m.theme.Terminal
	header := t.HeaderVal.Render("  GPIO pins") + "  " +
		t.Dim.Render("(read-only · enter/r reads · esc back)")

	rows := (len(gpioPins) + gpioGridCols - 1) / gpioGridCols
	cursorStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(t.Bg).
		Background(t.Accent).
		Padding(0, 1)
	cellStyle := lipgloss.NewStyle().
		Foreground(t.Text).
		Padding(0, 1)
	dimCell := lipgloss.NewStyle().
		Foreground(t.TextDim).
		Padding(0, 1)

	var b strings.Builder
	b.WriteString(header)
	b.WriteString("\n\n")

	for row := 0; row < rows; row++ {
		// Two lines per cell row: pin name, then state.
		var nameLine, stateLine strings.Builder
		for col := 0; col < gpioGridCols; col++ {
			idx := row*gpioGridCols + col
			if idx >= len(gpioPins) {
				continue
			}
			pin := gpioPins[idx]
			st := m.gpio.pins[pin]
			active := idx == m.gpio.cursor

			pinText := fmt.Sprintf("%-6s", pin)
			stateText := fmt.Sprintf("%-6s", gpioStateLabel(st))

			var pinCell, stateCell string
			switch {
			case active:
				pinCell = cursorStyle.Render(pinText)
				stateCell = cursorStyle.Render(stateText)
			case st != nil && (st.HasValue || st.LastErr != "" || st.Reading):
				pinCell = cellStyle.Render(pinText)
				stateCell = cellStyle.Render(stateText)
			default:
				pinCell = cellStyle.Render(pinText)
				stateCell = dimCell.Render(stateText)
			}
			nameLine.WriteString("    ")
			nameLine.WriteString(pinCell)
			stateLine.WriteString("    ")
			stateLine.WriteString(stateCell)
		}
		b.WriteString(nameLine.String())
		b.WriteString("\n")
		b.WriteString(stateLine.String())
		b.WriteString("\n\n")
	}

	_ = width
	return b.String()
}

// gpioStateLabel returns the short text shown beneath a pin name: a 0/1
// digit, "…" while a read is in flight, "err" on failure, or "—" before any
// read has happened.
func gpioStateLabel(st *pinState) string {
	if st == nil {
		return "—"
	}
	if st.Reading {
		return "…"
	}
	if st.LastErr != "" {
		return "err"
	}
	if st.HasValue {
		return st.LastValue
	}
	return "—"
}

// handleGPIOKey routes key events while the GPIO sub-view is active.
func (m *Model) handleGPIOKey(k tea.KeyPressMsg) (tui.DeviceView, tea.Cmd) {
	switch k.String() {
	case "esc":
		m.mode = modeMenu
		return m, nil
	case "up", "k":
		m.gpio.move(0, -1)
		return m, nil
	case "down", "j":
		m.gpio.move(0, 1)
		return m, nil
	case "left", "h":
		m.gpio.move(-1, 0)
		return m, nil
	case "right", "l":
		m.gpio.move(1, 0)
		return m, nil
	case "enter", "r", "R":
		pin := m.gpio.selectedPin()
		if pin == "" {
			return m, nil
		}
		return m, m.startGPIORead(pin)
	}
	return m, nil
}
