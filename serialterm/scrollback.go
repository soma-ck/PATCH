package serialterm

import (
	"bytes"
	"strconv"
)

// defaultScrollbackCapacity caps the terminal history to prevent unbounded
// memory growth. Overridable per Scrollback via NewScrollback.
const defaultScrollbackCapacity = 4000

// maxCol guards against pathological CUF / CHA values that would otherwise
// cause unbounded space-padding in cells. 4096 fits any reasonable terminal.
const maxCol = 4096

// Scrollback accumulates bytes from the serial device into terminal-friendly
// lines. It is a line-mode emulator with enough VT100 to keep modern CLIs
// (Flipper Zero, Linux shells) drawing correctly:
//
//   - Visible bytes are stored as a slice of cells indexed by visible column.
//     A cursor (col) tracks where the next write goes.
//   - SGR sequences (\e[...m) are remembered as transitions tied to specific
//     cells, so colors render correctly even after mid-line overwrites and
//     carry across line breaks.
//   - The following CSI sequences are honored so that line editors which
//     redraw via cursor moves rather than \b \b work correctly:
//       CUB  \e[<n>D       cursor left
//       CUF  \e[<n>C       cursor right
//       CHA  \e[<n>G       cursor horizontal absolute (1-indexed)
//       EL   \e[<n>K       erase in line (0=to end, 1=to start, 2=all)
//   - Other CSI sequences (cursor up/down, CUP, scroll, save/restore) are
//     consumed but discarded; they don't apply in a line-mode model.
//   - LF commits a line. CRLF counts as LF only. Lone CR clears the current
//     line back to the carried-forward style — a deliberate simplification
//     that handles prompt redraws and progress bars without needing row
//     tracking.
//   - \b is treated as cursor-left, and additionally truncates the trailing
//     cell if the cursor was at end-of-line. This is non-standard but
//     preserves backwards-compatible behavior for the (rare) device that
//     emits a bare \b for backspace echo.
type Scrollback struct {
	lines []string

	// In-progress line. cells holds the visible bytes; sgrAt[i] is the SGR
	// sequence to emit BEFORE cells[i] when rendering. col is the cursor
	// position and may equal len(cells) (end-of-line) or sit below it
	// (mid-line, after a cursor move).
	cells      []byte
	sgrAt      map[int][]byte
	col        int
	pendingSGR []byte // SGR seen but not yet attached to a written cell
	activeSGR  []byte // last non-reset SGR (carries across line breaks)

	gotCR   bool
	state   ansiState
	pending []byte

	capacity int
}

type ansiState int

const (
	stateNormal ansiState = iota
	stateEsc
	stateCSI
)

// NewScrollback creates a Scrollback with the given line-capacity limit. When
// capacity <= 0 the default (4000 lines) is used.
func NewScrollback(capacity int) *Scrollback {
	if capacity <= 0 {
		capacity = defaultScrollbackCapacity
	}
	return &Scrollback{capacity: capacity, sgrAt: map[int][]byte{}}
}

// Write feeds bytes through the state machine.
func (s *Scrollback) Write(p []byte) {
	for _, b := range p {
		s.feed(b)
	}
}

// Lines returns committed lines plus (if non-empty) the in-progress line.
// A line that contains nothing but a carry-forward SGR prefix is omitted so
// it doesn't render as a trailing blank line.
func (s *Scrollback) Lines() []string {
	if len(s.cells) == 0 {
		return s.lines
	}
	out := make([]string, 0, len(s.lines)+1)
	out = append(out, s.lines...)
	out = append(out, s.renderInProgress())
	return out
}

// renderInProgress builds the current cells + pending SGR into a string.
func (s *Scrollback) renderInProgress() string {
	var b bytes.Buffer
	for i, c := range s.cells {
		if sgr, ok := s.sgrAt[i]; ok {
			b.Write(sgr)
		}
		b.WriteByte(c)
	}
	if len(s.pendingSGR) > 0 {
		b.Write(s.pendingSGR)
	}
	return b.String()
}

// Clear resets all scrollback state.
func (s *Scrollback) Clear() {
	s.lines = nil
	s.cells = s.cells[:0]
	s.sgrAt = map[int][]byte{}
	s.col = 0
	s.pendingSGR = s.pendingSGR[:0]
	s.activeSGR = s.activeSGR[:0]
	s.gotCR = false
	s.state = stateNormal
	s.pending = s.pending[:0]
}

func (s *Scrollback) feed(b byte) {
	// Resolve a pending CR when the next byte arrives.
	if s.gotCR && b != '\n' && s.state == stateNormal {
		s.resetLine()
		s.gotCR = false
	}
	switch s.state {
	case stateNormal:
		s.feedNormal(b)
	case stateEsc:
		s.feedEsc(b)
	case stateCSI:
		s.feedCSI(b)
	}
}

func (s *Scrollback) feedNormal(b byte) {
	switch b {
	case 0x1b:
		s.state = stateEsc
		s.pending = append(s.pending[:0], b)
	case '\n':
		s.commitLine()
		s.gotCR = false
	case '\r':
		s.gotCR = true
	case '\b':
		// Cursor left, plus drop the trailing cell when the cursor was
		// sitting at end-of-line (legacy "destructive backspace" pattern
		// used by devices that don't emit \e[K after \b).
		if s.col > 0 {
			wasAtEnd := s.col == len(s.cells)
			s.col--
			if wasAtEnd {
				s.truncateCellsTo(s.col)
			}
		}
	case '\t':
		for range 4 {
			s.writeVisible(' ')
		}
	case 0x07:
		// BEL — drop silently.
	default:
		if b >= 0x20 {
			s.writeVisible(b)
		}
	}
}

func (s *Scrollback) feedEsc(b byte) {
	s.pending = append(s.pending, b)
	switch b {
	case '[':
		s.state = stateCSI
	default:
		s.state = stateNormal
		s.pending = s.pending[:0]
	}
}

func (s *Scrollback) feedCSI(b byte) {
	s.pending = append(s.pending, b)
	if b >= 0x40 && b <= 0x7E {
		switch b {
		case 'm':
			s.handleSGR()
		case 'D':
			n := csiParam(s.pending, 1)
			s.col -= n
			if s.col < 0 {
				s.col = 0
			}
		case 'C':
			n := csiParam(s.pending, 1)
			s.col += n
			if s.col > maxCol {
				s.col = maxCol
			}
		case 'G':
			// CHA params are 1-indexed.
			n := csiParam(s.pending, 1)
			s.col = min(max(n-1, 0), maxCol)
		case 'K':
			s.handleEL(csiParam(s.pending, 0))
		}
		s.state = stateNormal
		s.pending = s.pending[:0]
	}
}

func (s *Scrollback) handleSGR() {
	seq := append([]byte(nil), s.pending...)
	if isSGRReset(seq) {
		s.activeSGR = s.activeSGR[:0]
	} else {
		s.activeSGR = append(s.activeSGR[:0], seq...)
	}
	s.pendingSGR = append(s.pendingSGR[:0], seq...)
}

func (s *Scrollback) handleEL(mode int) {
	switch mode {
	case 0:
		s.truncateCellsTo(s.col)
	case 1:
		// Erase from start to cursor inclusive — replace with spaces so the
		// cursor column stays meaningful for any subsequent overwrite.
		end := min(s.col, len(s.cells)-1)
		for i := 0; i <= end; i++ {
			s.cells[i] = ' '
			delete(s.sgrAt, i)
		}
	case 2:
		s.cells = s.cells[:0]
		s.sgrAt = map[int][]byte{}
	}
}

func (s *Scrollback) writeVisible(b byte) {
	if s.col > maxCol {
		return
	}
	// Any prior SGR transition at this position is being overwritten; clear
	// it so the new write picks up either pendingSGR or the previous cell's
	// effective style.
	delete(s.sgrAt, s.col)
	if len(s.pendingSGR) > 0 {
		s.sgrAt[s.col] = append([]byte(nil), s.pendingSGR...)
		s.pendingSGR = s.pendingSGR[:0]
	}
	if s.col < len(s.cells) {
		s.cells[s.col] = b
	} else {
		// CUF moved past end-of-line — pad with spaces, then write.
		for len(s.cells) < s.col {
			s.cells = append(s.cells, ' ')
		}
		s.cells = append(s.cells, b)
	}
	s.col++
}

// truncateCellsTo drops cells (and their SGR transitions) at index >= col.
func (s *Scrollback) truncateCellsTo(col int) {
	col = max(col, 0)
	if col >= len(s.cells) {
		return
	}
	s.cells = s.cells[:col]
	for k := range s.sgrAt {
		if k >= col {
			delete(s.sgrAt, k)
		}
	}
}

// resetLine clears the in-progress line back to the active carried style. The
// carried SGR becomes pendingSGR so the next visible write starts with it.
func (s *Scrollback) resetLine() {
	s.cells = s.cells[:0]
	s.sgrAt = map[int][]byte{}
	s.col = 0
	s.pendingSGR = append(s.pendingSGR[:0], s.activeSGR...)
}

func (s *Scrollback) commitLine() {
	s.lines = append(s.lines, s.renderInProgress())
	if len(s.lines) > s.capacity {
		drop := len(s.lines) - s.capacity
		s.lines = s.lines[drop:]
	}
	s.cells = s.cells[:0]
	s.sgrAt = map[int][]byte{}
	s.col = 0
	// Carry-forward SGR onto the first cell of the next line.
	s.pendingSGR = append(s.pendingSGR[:0], s.activeSGR...)
}

// csiParam parses the first numeric parameter from a CSI sequence ending in a
// final byte (e.g. "\e[12D" -> 12). Returns def when the param is missing or
// unparseable.
func csiParam(seq []byte, def int) int {
	if len(seq) < 3 {
		return def
	}
	params := seq[2 : len(seq)-1]
	// Strip private-marker / intermediate bytes to keep parsing simple.
	if i := bytes.IndexByte(params, ';'); i >= 0 {
		params = params[:i]
	}
	if len(params) == 0 {
		return def
	}
	if n, err := strconv.Atoi(string(params)); err == nil {
		return n
	}
	return def
}

func isSGRReset(seq []byte) bool {
	if len(seq) < 3 || seq[len(seq)-1] != 'm' {
		return false
	}
	params := seq[2 : len(seq)-1]
	if len(params) == 0 {
		return true
	}
	return bytes.Equal(params, []byte("0"))
}
