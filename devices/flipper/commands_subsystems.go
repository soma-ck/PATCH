package flipper

import (
	"fmt"
	"strings"

	"github.com/olivierpoupier/patch/tui"
)

// subsystemCommands registers the Subsystems group entries.
//
// Currently only the built-in `i2c` bus scanner is exposed: it's a one-shot
// command that prints a grid and returns to the main shell.
//
// The Flipper's subghz / nfc / ir CLI subcommands are intentionally NOT
// registered here. They look like one-shots but on stock firmware running
// `subghz`, `nfc`, or `ir` with no arguments enters an *interactive* plugin
// mode that takes over the serial pipe until ETX (ctrl+c) is received — the
// runner can't capture them as prompt-bounded responses, and worse, the
// device stays stuck in plugin mode until ETX arrives, breaking every
// subsequent menu command. They will return as proper sub-views once a
// streaming runner (chunk delivery + ETX-on-stop) lands.
func subsystemCommands() []FlipperCommand {
	return []FlipperCommand{
		{
			ID:     "i2c_scan",
			Label:  "i2c bus scan",
			Group:  "Subsystems",
			Cmd:    "i2c",
			Format: formatI2CScan,
		},
	}
}

// formatI2CScan parses the firmware's `i2c` command output (a 16x8 grid of
// "#" / "-" cells across rows 0x0..0x7 and columns 0x0..0xF) and renders a
// summary plus the detected addresses.
//
// Firmware emits:
//
//	Scanning external i2c on PC0(SCL)/PC1(SDA)
//	Clock: 100khz, 7bit address
//
//	   | 0 1 2 3 4 5 6 7 8 9 A B C D E F
//	  --+--------------------------------
//	  0 | - - - - ...
//	  ...
//	  7 | - - - - ...
//
// The `#` cells indicate a responding device at address (row << 4) + col.
func formatI2CScan(raw []byte, t *tui.TerminalTheme) string {
	addrs := parseI2CScan(raw)
	var b strings.Builder
	b.WriteString("  ")
	b.WriteString(t.HeaderVal.Render("I²C bus scan"))
	b.WriteString("  ")
	b.WriteString(t.Dim.Render("(external bus, PC0/PC1, 100kHz, 7-bit)"))
	b.WriteString("\n\n")

	if len(addrs) == 0 {
		b.WriteString(t.Dim.Render("  No devices responded."))
		b.WriteString("\n\n")
	} else {
		b.WriteString("  ")
		b.WriteString(t.HeaderKey.Render("Devices"))
		b.WriteString("  ")
		parts := make([]string, len(addrs))
		for i, a := range addrs {
			parts[i] = fmt.Sprintf("0x%02X", a)
		}
		b.WriteString(t.HeaderVal.Render(strings.Join(parts, "  ")))
		b.WriteString("\n\n")
	}

	// Append the original grid (cleaned of ANSI) below the summary so the
	// user sees what the firmware actually printed.
	b.WriteString(t.Dim.Render("  Raw firmware output:"))
	b.WriteString("\n")
	b.WriteString(formatRawText(raw, t))
	return b.String()
}

// parseI2CScan returns the list of 7-bit I²C addresses marked `#` in the
// firmware grid. Returns nil when no grid rows are detected (e.g. if the
// command failed before any output was emitted).
//
// Each grid row looks like: "  N | x x x ..." where x ∈ {#, -}. We anchor on
// the " | " separator so we don't false-positive on the column header line.
func parseI2CScan(raw []byte) []byte {
	var addrs []byte
	for _, ln := range trimEchoAndPrompt(rawLines(raw)) {
		clean := strings.TrimSpace(stripSGR(ln))
		// The column-header line ("| 0 1 2 ...") doesn't have a row digit
		// before the pipe, so it'll fail this check.
		barIdx := strings.Index(clean, "|")
		if barIdx <= 0 {
			continue
		}
		left := strings.TrimSpace(clean[:barIdx])
		right := clean[barIdx+1:]
		// left must be a single hex digit (0-7).
		if len(left) != 1 {
			continue
		}
		row, ok := parseHexNibble(left[0])
		if !ok || row > 0x7 {
			continue
		}
		// Walk the right side cell by cell. Cells are " #" / " -" pairs (or
		// "# " / "- " at column 0); be liberal and just count #/- in order.
		col := byte(0)
		for _, c := range right {
			switch c {
			case '#':
				addrs = append(addrs, byte(row<<4)|col)
				col++
			case '-':
				col++
			}
			if col > 0xF {
				break
			}
		}
	}
	return addrs
}

// parseHexNibble converts an ASCII hex digit to its numeric value.
func parseHexNibble(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}
