package flipper

import (
	"strings"
	"testing"
)

func TestEndsWithPromptSGR(t *testing.T) {
	buf := []byte("hello\r\n\x1b[31;1m>:\x1b[0m ")
	if !endsWithPrompt(buf) {
		t.Fatalf("expected SGR-wrapped prompt to be detected")
	}
}

func TestEndsWithPromptPlain(t *testing.T) {
	buf := []byte("hello\r\n>: ")
	if !endsWithPrompt(buf) {
		t.Fatalf("expected plain prompt to be detected")
	}
}

func TestEndsWithPromptIgnoresMidBufferOccurrence(t *testing.T) {
	// The prompt token appears mid-stream but the buffer doesn't end with it
	// — must not falsely terminate.
	buf := []byte(">: device_info\r\nhardware_ver : 13\r\nmore output here")
	if endsWithPrompt(buf) {
		t.Fatalf("must not detect prompt in middle of buffer")
	}
}

func TestEndsWithPromptToleratesTrailingWhitespace(t *testing.T) {
	buf := []byte("\x1b[31;1m>:\x1b[0m\r\n")
	if !endsWithPrompt(buf) {
		t.Fatalf("expected prompt followed by CRLF to be detected")
	}
}

func TestRunnerCapturesCompleteResponse(t *testing.T) {
	var r commandRunner
	cmd := r.start("device_info")
	if cmd == nil {
		t.Fatal("start should return a timeout cmd")
	}
	if !r.busy() {
		t.Fatal("runner should be busy after start")
	}

	// Feed in two chunks; first chunk has no prompt, second completes it.
	if got := r.feed([]byte("device_info\r\nhardware_ver : 13\r\n")); got != nil {
		t.Fatalf("partial feed must not emit result")
	}
	finishCmd := r.feed([]byte("firmware_version : 0.95.1\r\n\x1b[31;1m>:\x1b[0m "))
	if finishCmd == nil {
		t.Fatal("feed with terminating prompt must emit result")
	}
	msg := finishCmd().(commandResultMsg)
	if msg.ID != "device_info" {
		t.Errorf("ID: got %q, want %q", msg.ID, "device_info")
	}
	if msg.Err != nil {
		t.Errorf("Err: got %v, want nil", msg.Err)
	}
	if !strings.Contains(string(msg.Raw), "hardware_ver") {
		t.Errorf("Raw missing payload: %q", msg.Raw)
	}
	if r.busy() {
		t.Fatal("runner should be idle after delivering result")
	}
}

func TestRunnerIgnoresFeedWhenIdle(t *testing.T) {
	var r commandRunner
	if got := r.feed([]byte("anything")); got != nil {
		t.Fatal("idle runner must not respond to feed")
	}
}

func TestRunnerTimeoutEmitsErr(t *testing.T) {
	var r commandRunner
	r.start("info")
	r.feed([]byte("partial response without prompt"))
	timeoutCmd := r.timeout(commandTimeoutMsg{ID: "info", Gen: r.gen})
	if timeoutCmd == nil {
		t.Fatal("timeout on busy runner must emit result")
	}
	msg := timeoutCmd().(commandResultMsg)
	if msg.Err == nil {
		t.Fatal("timeout result must carry error")
	}
	if r.busy() {
		t.Fatal("runner should be idle after timeout")
	}
}

func TestRunnerStaleTimeoutNoOp(t *testing.T) {
	var r commandRunner
	r.start("info")
	staleGen := r.gen
	// Complete the command before the timeout fires.
	r.feed([]byte("ok\r\n>: "))
	// Now timeout fires for the now-completed command — must not double-emit.
	if got := r.timeout(commandTimeoutMsg{ID: "info", Gen: staleGen}); got != nil {
		t.Fatal("stale timeout must be a no-op when runner has finished")
	}
}

func TestRunnerNewCommandSupersedesOldTimeout(t *testing.T) {
	var r commandRunner
	r.start("first")
	staleGen := r.gen
	r.start("second") // before "first" timed out
	if got := r.timeout(commandTimeoutMsg{ID: "first", Gen: staleGen}); got != nil {
		t.Fatal("timeout for superseded command must be a no-op")
	}
	if !r.busy() {
		t.Fatal("runner should still be running the second command")
	}
}
