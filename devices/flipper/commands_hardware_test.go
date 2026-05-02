package flipper

import (
	"strings"
	"testing"

	"github.com/olivierpoupier/patch/tui"
)

func TestHardwareCommandsRegistered(t *testing.T) {
	want := []string{
		"led_red_on", "led_red_off",
		"led_green_on", "led_green_off",
		"led_blue_on", "led_blue_off",
		"backlight_on", "backlight_off",
		"vibro_pulse",
	}
	cmds := flipperCommands()
	have := make(map[string]FlipperCommand, len(cmds))
	for _, c := range cmds {
		have[c.ID] = c
	}
	for _, id := range want {
		if _, ok := have[id]; !ok {
			t.Errorf("hardware command %q not registered", id)
		}
	}
}

func TestLEDCommandSyntax(t *testing.T) {
	// Spot-check that on/off variants emit the firmware-expected level.
	cases := []struct {
		id   string
		want string
	}{
		{"led_red_on", "led r 255"},
		{"led_red_off", "led r 0"},
		{"led_green_on", "led g 255"},
		{"backlight_on", "led bl 255"},
		{"backlight_off", "led bl 0"},
	}
	for _, c := range cases {
		got := findCommand(c.id)
		if got == nil {
			t.Errorf("findCommand(%q) returned nil", c.id)
			continue
		}
		if got.Cmd != c.want {
			t.Errorf("%s.Cmd = %q, want %q", c.id, got.Cmd, c.want)
		}
	}
}

func TestVibroPulseHasFollowUp(t *testing.T) {
	got := findCommand("vibro_pulse")
	if got == nil {
		t.Fatal("vibro_pulse not registered")
	}
	if got.Cmd != "vibro 1" {
		t.Errorf("vibro_pulse.Cmd = %q, want %q", got.Cmd, "vibro 1")
	}
	if got.FollowUp != "vibro 0" {
		t.Errorf("vibro_pulse.FollowUp = %q, want %q", got.FollowUp, "vibro 0")
	}
	if got.FollowUpDelay <= 0 {
		t.Errorf("vibro_pulse.FollowUpDelay = %v, want > 0", got.FollowUpDelay)
	}
}

func TestFormatActionResponseSilentSuccess(t *testing.T) {
	theme := tui.NewTheme().Terminal
	// LED commands print nothing on success — just the echoed cmd + prompt.
	raw := []byte("\x1b[31;1m>:\x1b[0m led r 255\r\n\x1b[31;1m>:\x1b[0m ")
	out := formatActionResponse("LED red on")(raw, theme)
	if !strings.Contains(out, "✓ LED red on") {
		t.Errorf("expected checkmark line, got:\n%s", out)
	}
	if strings.Contains(out, "Device response") {
		t.Errorf("silent success should not show 'Device response' subsection:\n%s", out)
	}
}

func TestFormatActionResponseShowsErrorBody(t *testing.T) {
	theme := tui.NewTheme().Terminal
	// Stealth-mode rejection from the firmware's vibro handler.
	raw := []byte("\x1b[31;1m>:\x1b[0m vibro 1\r\n" +
		"Flipper is in stealth mode. Unmute the device to control vibration.\r\n" +
		"\x1b[31;1m>:\x1b[0m ")
	out := formatActionResponse("Vibro pulse")(raw, theme)
	if !strings.Contains(out, "Device response") {
		t.Errorf("error body should trigger 'Device response' subsection:\n%s", out)
	}
	if !strings.Contains(out, "stealth mode") {
		t.Errorf("device error text should be preserved:\n%s", out)
	}
}

func TestHasActionResponseBody(t *testing.T) {
	silent := []byte("\x1b[31;1m>:\x1b[0m led r 0\r\n\x1b[31;1m>:\x1b[0m ")
	if hasActionResponseBody(silent) {
		t.Error("silent response should report no body")
	}
	withBody := []byte(">: vibro 1\r\nstealth mode\r\n>: ")
	if !hasActionResponseBody(withBody) {
		t.Error("response with body should be detected")
	}
}
