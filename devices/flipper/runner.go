package flipper

import (
	"bytes"
	"time"

	tea "charm.land/bubbletea/v2"
)

// runnerTimeout caps how long a single command can run before we give up.
// Real Flipper responses for the commands we issue arrive in well under a
// second; the timeout is mostly a safety net for a wedged device.
const runnerTimeout = 3 * time.Second

// commandRunner manages one in-flight Flipper CLI command at a time. It
// accumulates RX bytes until the Flipper prompt is observed at end-of-buffer,
// then emits a commandResultMsg.
//
// Replaces the old fixed-duration capture window: the prompt is the canonical
// "command finished" signal the firmware itself emits, so we don't need to
// guess a wait time.
type commandRunner struct {
	id     string  // empty when idle
	buf    []byte  // accumulated RX (including the echoed command line)
	finish bool    // already emitted commandResultMsg for this run
	gen    int     // monotonic counter — guards against stale timeout messages
}

// commandResultMsg is delivered to the model when a command's response is
// fully captured. Raw is the entire RX between command-issue and the trailing
// prompt; Err is non-nil if the runner timed out.
type commandResultMsg struct {
	ID  string
	Raw []byte
	Err error
}

// commandTimeoutMsg fires after runnerTimeout. It's a no-op if the runner has
// already finished or moved on to a newer command.
type commandTimeoutMsg struct {
	ID  string
	Gen int
}

// busy reports whether a command is in flight.
func (r *commandRunner) busy() bool {
	return r != nil && r.id != "" && !r.finish
}

// start arms the runner for a new command. The caller is responsible for
// actually sending the command bytes; start only sets up the capture state.
// Returns a tea.Cmd that fires the timeout safety net.
func (r *commandRunner) start(id string) tea.Cmd {
	r.id = id
	r.buf = r.buf[:0]
	r.finish = false
	r.gen++
	gen := r.gen
	return tea.Tick(runnerTimeout, func(time.Time) tea.Msg {
		return commandTimeoutMsg{ID: id, Gen: gen}
	})
}

// feed appends RX bytes and, if the prompt has arrived, returns a
// commandResultMsg via the returned tea.Cmd. Returns nil otherwise.
//
// The prompt boundary is detected on the *trailing* end of the accumulated
// buffer. Looking for the prompt anywhere would falsely terminate on commands
// like `log` whose body can contain ">: " sequences in echoed text.
func (r *commandRunner) feed(data []byte) tea.Cmd {
	if !r.busy() {
		return nil
	}
	r.buf = append(r.buf, data...)
	if !endsWithPrompt(r.buf) {
		return nil
	}
	id := r.id
	raw := append([]byte(nil), r.buf...)
	r.finish = true
	r.id = ""
	r.buf = r.buf[:0]
	return func() tea.Msg {
		return commandResultMsg{ID: id, Raw: raw}
	}
}

// timeout handles a commandTimeoutMsg. If the runner is still on the same
// generation and has not finished, it emits a commandResultMsg with the
// partial buffer and a timeout error.
func (r *commandRunner) timeout(msg commandTimeoutMsg) tea.Cmd {
	if r.gen != msg.Gen || r.finish || r.id != msg.ID {
		return nil
	}
	id := r.id
	raw := append([]byte(nil), r.buf...)
	r.finish = true
	r.id = ""
	r.buf = r.buf[:0]
	return func() tea.Msg {
		return commandResultMsg{ID: id, Raw: raw, Err: errCommandTimeout{cmd: id}}
	}
}

// reset discards any in-flight state. Used on disconnect.
func (r *commandRunner) reset() {
	r.id = ""
	r.buf = r.buf[:0]
	r.finish = false
	r.gen++
}

type errCommandTimeout struct{ cmd string }

func (e errCommandTimeout) Error() string {
	// The most common cause is the device entered an interactive plugin
	// (subghz/nfc/ir) instead of returning a prompt. Hint at the recovery
	// path so users aren't left guessing.
	return "no prompt received within " + runnerTimeout.String() +
		" — device may be in an interactive subsystem; press esc to return to menu (ETX will be sent to recover)"
}

// promptMarkers lists every byte sequence the Flipper emits as a prompt
// terminator. The firmware uses an SGR-wrapped form ("\e[31;1m>:\e[0m "), but
// the prompt may also appear as plain ">: " in some firmware builds and after
// the user pastes input. We accept both.
var promptMarkers = [][]byte{
	[]byte("\x1b[31;1m>:\x1b[0m "),
	[]byte("\x1b[31;1m>:\x1b[0m"),
	[]byte(">: "),
}

// endsWithPrompt reports whether the buffer ends with a Flipper prompt,
// possibly followed by trailing whitespace. The whitespace tolerance handles
// firmware variations and bytes that arrive in oddly chunked reads.
func endsWithPrompt(buf []byte) bool {
	end := len(buf)
	for end > 0 {
		c := buf[end-1]
		if c == ' ' || c == '\t' || c == '\r' || c == '\n' {
			end--
			continue
		}
		break
	}
	if end == 0 {
		return false
	}
	for _, marker := range promptMarkers {
		if bytes.HasSuffix(buf[:end], bytes.TrimRight(marker, " ")) {
			return true
		}
	}
	return false
}
