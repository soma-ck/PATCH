package flipper

import (
	"strings"
	"time"

	"github.com/olivierpoupier/patch/tui"
)

// vibroPulseDuration is how long the vibration is on for a "pulse" action
// before the auto-issued `vibro 0` stops it. Long enough to feel, short
// enough to not be annoying.
const vibroPulseDuration = 200 * time.Millisecond

// hardwareCommands registers the Phase 3 LED and vibro entries. These are
// fire-and-forget actions: the Flipper firmware prints nothing on success, so
// the detail pane shows "Action sent" unless the device replies with an error
// (e.g., stealth mode rejecting vibro, invalid LED arguments).
func hardwareCommands() []FlipperCommand {
	cmds := []FlipperCommand{}
	for _, c := range []struct {
		id, label, code string
	}{
		{"led_red_on", "LED red on", "r"},
		{"led_red_off", "LED red off", "r"},
		{"led_green_on", "LED green on", "g"},
		{"led_green_off", "LED green off", "g"},
		{"led_blue_on", "LED blue on", "b"},
		{"led_blue_off", "LED blue off", "b"},
		{"backlight_on", "Backlight on", "bl"},
		{"backlight_off", "Backlight off", "bl"},
	} {
		level := "255"
		if strings.HasSuffix(c.id, "_off") {
			level = "0"
		}
		cmds = append(cmds, FlipperCommand{
			ID:     c.id,
			Label:  c.label,
			Group:  "Hardware",
			Cmd:    "led " + c.code + " " + level,
			Format: formatActionResponse(c.label),
		})
	}
	cmds = append(cmds, FlipperCommand{
		ID:            "vibro_pulse",
		Label:         "Vibro pulse",
		Group:         "Hardware",
		Cmd:           "vibro 1",
		FollowUp:      "vibro 0",
		FollowUpDelay: vibroPulseDuration,
		Format:        formatActionResponse("Vibro pulse"),
	})
	return cmds
}

// formatActionResponse returns a Format function for hardware actions: when
// the firmware sends no output (the command succeeded silently), display a
// "✓ <label>" confirmation; otherwise show the firmware's response unchanged
// so users can see error messages like stealth-mode rejection.
func formatActionResponse(label string) func([]byte, *tui.TerminalTheme) string {
	return func(raw []byte, t *tui.TerminalTheme) string {
		if !hasActionResponseBody(raw) {
			return "  " + t.HeaderVal.Render("✓ "+label)
		}
		return "  " + t.HeaderVal.Render("✓ "+label) + "\n\n" +
			"  " + t.Body.Render("Device response:") + "\n" +
			formatRawText(raw, t)
	}
}

// hasActionResponseBody reports whether the firmware printed anything beyond
// the echoed command and the trailing prompt. Used to decide whether to show
// just the success checkmark or also a "Device response:" subsection.
func hasActionResponseBody(raw []byte) bool {
	for _, ln := range trimEchoAndPrompt(rawLines(raw)) {
		if strings.TrimSpace(stripSGR(ln)) != "" {
			return true
		}
	}
	return false
}
