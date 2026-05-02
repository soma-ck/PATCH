package flipper

import (
	"strings"
	"testing"

	"github.com/olivierpoupier/patch/tui"
)

func TestFormatUptimeHumanizes(t *testing.T) {
	theme := tui.NewTheme().Terminal
	raw := []byte(">: uptime\r\nUptime: 1h23m45s\r\n>: ")
	out := formatUptime(raw, theme)
	for _, want := range []string{"1 hour", "23 minute", "45 second", "5025"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in output:\n%s", want, out)
		}
	}
}

func TestFormatUptimeZero(t *testing.T) {
	theme := tui.NewTheme().Terminal
	raw := []byte("Uptime: 0h0m0s")
	out := formatUptime(raw, theme)
	// 0 seconds — singular form for the seconds line.
	if !strings.Contains(out, "0 seconds") {
		t.Errorf("expected '0 seconds', got:\n%s", out)
	}
}

func TestFormatUptimeUnparseableFallsBack(t *testing.T) {
	theme := tui.NewTheme().Terminal
	raw := []byte(">: uptime\r\nWeird format here\r\n>: ")
	out := formatUptime(raw, theme)
	if !strings.Contains(out, "Weird format here") {
		t.Errorf("fallback should preserve original text, got:\n%s", out)
	}
}

func TestFormatDateParses(t *testing.T) {
	theme := tui.NewTheme().Terminal
	raw := []byte(">: date\r\n2026-05-02 14:23:45 5\r\n>: ")
	out := formatDate(raw, theme)
	for _, want := range []string{"2026-05-02", "14:23:45", "Friday"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in output:\n%s", want, out)
		}
	}
}

func TestFormatDateInvalidWeekdayShowsDash(t *testing.T) {
	theme := tui.NewTheme().Terminal
	raw := []byte("2026-05-02 14:23:45 99")
	out := formatDate(raw, theme)
	if !strings.Contains(out, "—") {
		t.Errorf("invalid weekday should yield em dash, got:\n%s", out)
	}
}

func TestFormatFreeRendersAllFields(t *testing.T) {
	theme := tui.NewTheme().Terminal
	raw := []byte("" +
		">: free\r\n" +
		"Free heap size: 154296\r\n" +
		"Total heap size: 196608\r\n" +
		"Minimum heap size: 142400\r\n" +
		"Maximum heap block: 96512\r\n" +
		"Pool free: 8192\r\n" +
		"Maximum pool block: 4096\r\n" +
		">: ")
	out := formatFree(raw, theme)
	wants := []string{
		"Free heap size", "154296 bytes", "150.7 KB",
		"Total heap size", "196608 bytes", "192.0 KB",
		"Pool free", "8192 bytes",
	}
	for _, want := range wants {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in output:\n%s", want, out)
		}
	}
}

func TestFormatBytesUnits(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"512", "512 bytes"},
		{"1024", "1024 bytes (1.0 KB)"},
		{"1048576", "1048576 bytes (1.00 MB)"},
		{"not-a-number", "not-a-number"},
	}
	for _, c := range cases {
		if got := formatBytes(c.in); got != c.want {
			t.Errorf("formatBytes(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFormatRawTextStripsEchoAndPrompt(t *testing.T) {
	theme := tui.NewTheme().Terminal
	raw := []byte("" +
		"\x1b[31;1m>:\x1b[0m free_blocks\r\n" +
		"  block #1: 4096\r\n" +
		"  block #2: 2048\r\n" +
		"\x1b[31;1m>:\x1b[0m ")
	out := formatRawText(raw, theme)
	if strings.Contains(out, "free_blocks") {
		t.Errorf("output should not include echoed command line:\n%s", out)
	}
	if !strings.Contains(out, "block #1") {
		t.Errorf("output should retain body:\n%s", out)
	}
	if !strings.Contains(out, "block #2") {
		t.Errorf("output should retain body:\n%s", out)
	}
	// Trailing bare-prompt line must be dropped.
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	last := strings.TrimSpace(stripSGR(lines[len(lines)-1]))
	if last == ">:" || last == ">" {
		t.Errorf("last visible line should not be a bare prompt: %q", last)
	}
}

func TestFormatRawTextHandlesAnsiCursorEscapes(t *testing.T) {
	theme := tui.NewTheme().Terminal
	// Simulate `top 0`-ish output with cursor moves and erase escapes that the
	// scrollback emulator should strip.
	raw := []byte("" +
		">: top 0\r\n" +
		"Threads: 12, Uptime: 0h1m23s\x1b[0K\r\n" +
		"Heap: total 196608, free 154296\x1b[0K\r\n" +
		"\x1b[0K\r\n" +
		"AppID Name State\x1b[0K\r\n" +
		"sys   gui  Ready\x1b[0K\r\n" +
		"\x1b[J" +
		">: ")
	out := formatRawText(raw, theme)
	for _, want := range []string{"Threads: 12", "Heap: total", "AppID", "sys"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in output:\n%s", want, out)
		}
	}
	if strings.Contains(out, "\x1b[0K") || strings.Contains(out, "\x1b[J") {
		t.Errorf("ANSI escapes should be stripped, got:\n%q", out)
	}
}

func TestSystemCommandsRegistered(t *testing.T) {
	got := flipperCommands()
	wantIDs := []string{"uptime", "date", "free", "free_blocks", "top"}
	for _, id := range wantIDs {
		found := false
		for _, c := range got {
			if c.ID == id {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("command %q not registered in flipperCommands()", id)
		}
	}
}
