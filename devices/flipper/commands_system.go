package flipper

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/olivierpoupier/patch/serialterm"
	"github.com/olivierpoupier/patch/tui"
)

// systemCommands returns the Phase 2 dashboard entries: uptime, date, memory,
// heap blocks, and a one-shot top snapshot. `log` is intentionally omitted —
// it streams until CTRL+C and needs a different UX than a one-shot menu
// command.
func systemCommands() []FlipperCommand {
	return []FlipperCommand{
		{
			ID:     "uptime",
			Label:  "Uptime",
			Group:  "System",
			Cmd:    "uptime",
			Format: formatUptime,
		},
		{
			ID:     "date",
			Label:  "Date & time",
			Group:  "System",
			Cmd:    "date",
			Format: formatDate,
		},
		{
			ID:     "free",
			Label:  "Memory",
			Group:  "System",
			Cmd:    "free",
			Format: formatFree,
		},
		{
			ID:     "free_blocks",
			Label:  "Heap blocks",
			Group:  "System",
			Cmd:    "free_blocks",
			Format: formatRawText,
		},
		{
			ID:     "top",
			Label:  "Threads (top)",
			Group:  "System",
			Cmd:    "top 0", // 0 = single snapshot; default would loop forever
			Format: formatRawText,
		},
	}
}

// uptimeRe matches the firmware's `uptime` output: "Uptime: NhNmNs" with no
// space separators (e.g. "Uptime: 1h23m45s").
var uptimeRe = regexp.MustCompile(`Uptime:\s*(\d+)h(\d+)m(\d+)s`)

func formatUptime(raw []byte, t *tui.TerminalTheme) string {
	body := string(raw)
	m := uptimeRe.FindStringSubmatch(body)
	if m == nil {
		return formatRawText(raw, t)
	}
	hours, _ := strconv.Atoi(m[1])
	minutes, _ := strconv.Atoi(m[2])
	seconds, _ := strconv.Atoi(m[3])

	var humanized strings.Builder
	if hours > 0 {
		fmt.Fprintf(&humanized, "%d hour", hours)
		if hours != 1 {
			humanized.WriteByte('s')
		}
		humanized.WriteByte(' ')
	}
	if minutes > 0 || hours > 0 {
		fmt.Fprintf(&humanized, "%d minute", minutes)
		if minutes != 1 {
			humanized.WriteByte('s')
		}
		humanized.WriteByte(' ')
	}
	fmt.Fprintf(&humanized, "%d second", seconds)
	if seconds != 1 {
		humanized.WriteByte('s')
	}

	totalSec := hours*3600 + minutes*60 + seconds
	var b strings.Builder
	b.WriteString("  ")
	b.WriteString(t.HeaderKey.Render("Uptime  "))
	b.WriteString("  ")
	b.WriteString(t.HeaderVal.Render(humanized.String()))
	b.WriteString("\n  ")
	b.WriteString(t.HeaderKey.Render("Seconds "))
	b.WriteString("  ")
	b.WriteString(t.HeaderVal.Render(strconv.Itoa(totalSec)))
	b.WriteString("\n")
	return b.String()
}

// dateRe matches the firmware's `date` output:
//
//	YYYY-MM-DD HH:MM:SS W
//
// where W is a 1-based weekday (1 = Monday … 7 = Sunday).
var dateRe = regexp.MustCompile(`(\d{4})-(\d{2})-(\d{2})\s+(\d{2}):(\d{2}):(\d{2})\s+(\d+)`)

var weekdayNames = [...]string{"", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday", "Sunday"}

func formatDate(raw []byte, t *tui.TerminalTheme) string {
	body := string(raw)
	m := dateRe.FindStringSubmatch(body)
	if m == nil {
		return formatRawText(raw, t)
	}
	weekday := "—"
	if w, err := strconv.Atoi(m[7]); err == nil && w >= 1 && w <= 7 {
		weekday = weekdayNames[w]
	}
	rows := [][2]string{
		{"Date", m[1] + "-" + m[2] + "-" + m[3]},
		{"Time", m[4] + ":" + m[5] + ":" + m[6]},
		{"Weekday", weekday},
	}
	return renderLabeledRows(t, rows)
}

// freeFieldOrder lists the `free` command's lines in the order the firmware
// emits them. Used so the formatter renders a stable layout instead of map
// iteration order.
var freeFieldOrder = []string{
	"Free heap size",
	"Total heap size",
	"Minimum heap size",
	"Maximum heap block",
	"Pool free",
	"Maximum pool block",
}

func formatFree(raw []byte, t *tui.TerminalTheme) string {
	fields := parseFlipperKV(raw)
	if len(fields) == 0 {
		return formatRawText(raw, t)
	}
	rows := make([][2]string, 0, len(freeFieldOrder))
	for _, key := range freeFieldOrder {
		val, ok := fields[key]
		if !ok {
			continue
		}
		rows = append(rows, [2]string{key, formatBytes(val)})
	}
	if len(rows) == 0 {
		return formatRawText(raw, t)
	}
	return renderLabeledRows(t, rows)
}

// formatRawText runs the raw bytes through a Scrollback to strip ANSI cursor
// movements and erase escapes, drops the echoed command line and the trailing
// prompt, and renders the rest as-is. Used for commands that emit free-form
// output we don't yet have a structured parser for.
func formatRawText(raw []byte, t *tui.TerminalTheme) string {
	sb := serialterm.NewScrollback(0)
	sb.Write(raw)
	lines := sb.Lines()
	lines = trimEchoAndPrompt(lines)
	if len(lines) == 0 {
		return t.Dim.Render("  (no output)")
	}
	var b strings.Builder
	for _, ln := range lines {
		b.WriteString("  ")
		b.WriteString(t.Body.Render(ln))
		b.WriteString("\n")
	}
	return b.String()
}

// trimEchoAndPrompt drops the leading echoed command (a line starting with the
// prompt token ">:" or containing ":") and the trailing line if it's a bare
// prompt.
func trimEchoAndPrompt(lines []string) []string {
	// Drop leading echoed-command lines.
	for len(lines) > 0 {
		first := strings.TrimSpace(stripSGR(lines[0]))
		if first == "" || strings.HasPrefix(first, ">:") || strings.HasPrefix(first, ">") {
			lines = lines[1:]
			continue
		}
		break
	}
	// Drop trailing prompt-only lines.
	for len(lines) > 0 {
		last := strings.TrimSpace(stripSGR(lines[len(lines)-1]))
		if last == "" || last == ">:" || last == ">" {
			lines = lines[:len(lines)-1]
			continue
		}
		break
	}
	return lines
}

// sgrRe matches CSI SGR sequences ("\e[...m") so we can normalise lines for
// text-comparison purposes (echo / prompt detection). Visible rendering still
// uses the unmodified bytes.
var sgrRe = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripSGR(s string) string { return sgrRe.ReplaceAllString(s, "") }

// renderLabeledRows renders a 2-column "Label   Value" block aligned on the
// label column, sharing the look of formatKVResponse.
func renderLabeledRows(t *tui.TerminalTheme, rows [][2]string) string {
	maxLabel := 0
	for _, r := range rows {
		if n := len(r[0]); n > maxLabel {
			maxLabel = n
		}
	}
	var b strings.Builder
	for _, r := range rows {
		pad := strings.Repeat(" ", maxLabel-len(r[0]))
		b.WriteString("  ")
		b.WriteString(t.HeaderKey.Render(r[0] + pad))
		b.WriteString("  ")
		b.WriteString(t.HeaderVal.Render(r[1]))
		b.WriteString("\n")
	}
	return b.String()
}

// formatBytes annotates a numeric byte count with a KB / MB approximation when
// the magnitude warrants it: "12345" → "12345 bytes (12.1 KB)".
func formatBytes(s string) string {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return s
	}
	if n < 1024 {
		return fmt.Sprintf("%d bytes", n)
	}
	if n < 1024*1024 {
		return fmt.Sprintf("%d bytes (%.1f KB)", n, float64(n)/1024)
	}
	return fmt.Sprintf("%d bytes (%.2f MB)", n, float64(n)/1024/1024)
}
