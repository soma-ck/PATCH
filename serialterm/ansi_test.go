package serialterm

import (
	"reflect"
	"strings"
	"testing"
)

func TestScrollbackPlainText(t *testing.T) {
	s := NewScrollback(0)
	s.Write([]byte("hello\nworld\n"))
	got := s.Lines()
	want := []string{"hello", "world"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("plain text: got %q, want %q", got, want)
	}
}

func TestScrollbackInProgressLine(t *testing.T) {
	s := NewScrollback(0)
	s.Write([]byte("one\ntwo"))
	got := s.Lines()
	want := []string{"one", "two"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("in-progress: got %q, want %q", got, want)
	}
}

func TestScrollbackSGRPassthrough(t *testing.T) {
	s := NewScrollback(0)
	s.Write([]byte("\x1b[31mRED\x1b[0m\n"))
	got := s.Lines()
	want := []string{"\x1b[31mRED\x1b[0m"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SGR passthrough: got %q, want %q", got, want)
	}
}

func TestScrollbackStripsCUP(t *testing.T) {
	// Cursor-position and erase escapes must be dropped.
	s := NewScrollback(0)
	s.Write([]byte("a\x1b[2;5Hb\x1b[Kc\n"))
	got := s.Lines()
	want := []string{"abc"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("CUP+EL strip: got %q, want %q", got, want)
	}
}

func TestScrollbackCRResetsVisible(t *testing.T) {
	// Our line-mode scanner treats CR as "redraw current line": visible
	// bytes are cleared and subsequent writes start at column 0. This gives
	// clean output for common cases like progress bars and prompt redraws
	// without needing a full VT emulator.
	s := NewScrollback(0)
	s.Write([]byte("first\rse\n"))
	got := s.Lines()
	want := []string{"se"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("CR reset: got %q, want %q", got, want)
	}
}

func TestScrollbackSGRResetStopsCarry(t *testing.T) {
	s := NewScrollback(0)
	s.Write([]byte("\x1b[31mRED\x1b[0m\nplain\n"))
	got := s.Lines()
	want := []string{"\x1b[31mRED\x1b[0m", "plain"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SGR reset stops carry: got %q, want %q", got, want)
	}
}

func TestScrollbackSGRCarriesAcrossLines(t *testing.T) {
	s := NewScrollback(0)
	s.Write([]byte("\x1b[31mRED\nstill-red\n"))
	got := s.Lines()
	want := []string{"\x1b[31mRED", "\x1b[31mstill-red"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SGR carry: got %q, want %q", got, want)
	}
}

func TestScrollbackBackspace(t *testing.T) {
	s := NewScrollback(0)
	s.Write([]byte("abc\b\bX\n"))
	got := s.Lines()
	want := []string{"aX"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("backspace: got %q, want %q", got, want)
	}
}

func TestScrollbackTab(t *testing.T) {
	s := NewScrollback(0)
	s.Write([]byte("a\tb\n"))
	got := s.Lines()
	want := []string{"a    b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tab expand: got %q, want %q", got, want)
	}
}

func TestScrollbackClear(t *testing.T) {
	s := NewScrollback(0)
	s.Write([]byte("a\nb\n"))
	s.Clear()
	if got := s.Lines(); len(got) != 0 {
		t.Fatalf("clear: expected empty, got %q", got)
	}
}

func TestScrollbackCapacityRing(t *testing.T) {
	s := NewScrollback(3)
	s.Write([]byte("a\nb\nc\nd\ne\n"))
	got := s.Lines()
	want := []string{"c", "d", "e"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ring: got %q, want %q", got, want)
	}
}

func TestScrollbackDefaultCapacity(t *testing.T) {
	// NewScrollback(0) picks the default; a negative value is equivalent.
	s := NewScrollback(-1)
	if s.capacity != defaultScrollbackCapacity {
		t.Fatalf("default capacity: got %d, want %d", s.capacity, defaultScrollbackCapacity)
	}
}

// TestScrollbackCSIBackspaceFlipper covers the echo pattern Flipper Zero
// emits for backspace: \e[1D + (rest of line) + \e[0K + (optional \e[ND).
// Without CUB+EL handling the deleted char would linger on screen.
func TestScrollbackCSIBackspaceFlipper(t *testing.T) {
	s := NewScrollback(0)
	s.Write([]byte("abc\x1b[1D\x1b[0K\n"))
	got := s.Lines()
	want := []string{"ab"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("CSI backspace EOL: got %q, want %q", got, want)
	}
}

// TestScrollbackCSIMidLineDelete covers Flipper's mid-line backspace echo:
// move left, redraw the rest, erase trailing, move cursor back.
func TestScrollbackCSIMidLineDelete(t *testing.T) {
	s := NewScrollback(0)
	// Initial line "abcd" with cursor at col 2 (between b and c) — the user
	// has just pressed backspace, so the firmware sends:
	//   \e[1D   cursor to col 1
	//   "cd"    redraw the rest of the line (overwrite c, then d)
	//   \e[0K   erase from cursor to end (drops the stale trailing 'd')
	//   \e[2D   cursor back to col 1
	s.Write([]byte("abcd\x1b[1D\x1b[1D\x1b[1Dcd\x1b[0K\x1b[2D\n"))
	got := s.Lines()
	want := []string{"acd"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("CSI mid-line delete: got %q, want %q", got, want)
	}
}

// TestScrollbackCHARedraw covers history-navigation redraw: cursor home, new
// content, erase trailing leftovers from the previous longer line.
func TestScrollbackCHARedraw(t *testing.T) {
	s := NewScrollback(0)
	s.Write([]byte("oldcommand\x1b[1Gnew\x1b[0K\n"))
	got := s.Lines()
	want := []string{"new"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("CHA redraw: got %q, want %q", got, want)
	}
}

// TestScrollbackCUFPadsWithSpaces covers cursor-right past end-of-line:
// subsequent writes pad the gap with spaces so column geometry is preserved.
func TestScrollbackCUFPadsWithSpaces(t *testing.T) {
	s := NewScrollback(0)
	s.Write([]byte("ab\x1b[3CX\n"))
	got := s.Lines()
	want := []string{"ab   X"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("CUF pad: got %q, want %q", got, want)
	}
}

// TestScrollbackELFromCursorClearsSGR ensures that when EL drops cells, any
// SGR transitions tied to those cells are dropped too, so the next write
// doesn't replay the stale red color onto the new content.
func TestScrollbackELFromCursorClearsSGR(t *testing.T) {
	s := NewScrollback(0)
	s.Write([]byte("\x1b[31mab\x1b[2D\x1b[0K\x1b[0mxy\n"))
	// Trace: red "ab", cursor back to col 0, erase to end, send reset,
	// then "xy". The 31m transition should not survive the erase.
	got := s.Lines()
	for _, line := range got {
		if strings.Contains(line, "\x1b[31m") {
			t.Fatalf("EL must drop stale red SGR; got %q", got)
		}
	}
}

func TestScrollbackANSISample(t *testing.T) {
	// Representative of a device reply intermixed with ANSI prompt
	// formatting. All cursor-movement escapes should be stripped, colours
	// should be kept, and plain lines should appear.
	input := []byte(
		"\x1b[31;1m>:\x1b[0m device_info\r\n" +
			"hardware_ver        : 13\r\n" +
			"firmware_version    : 0.95.1\r\n" +
			"\x1b[31;1m>:\x1b[0m ")
	s := NewScrollback(0)
	s.Write(input)
	lines := s.Lines()
	if len(lines) < 3 {
		t.Fatalf("expected at least 3 lines, got %d: %q", len(lines), lines)
	}
}
