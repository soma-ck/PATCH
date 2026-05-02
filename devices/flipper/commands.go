package flipper

import (
	"sort"
	"strings"
	"time"

	"github.com/olivierpoupier/patch/tui"
)

// FlipperCommand describes a single menu entry: the label shown to the user,
// the underlying CLI command sent to the device, and how to present the
// response in the detail pane.
type FlipperCommand struct {
	ID      string                                        // stable identifier, used in result messages
	Label   string                                        // menu label
	Group   string                                        // menu section header
	Cmd     string                                        // raw Flipper CLI command (no trailing CR/LF)
	Confirm bool                                          // require y/N confirmation before sending
	Format  func(raw []byte, t *tui.TerminalTheme) string // detail-pane renderer

	// FollowUp is a second CLI command issued FollowUpDelay after the first
	// one's response arrives. Used for "pulse" patterns like vibro on→off.
	// Both fields must be set for the follow-up to fire.
	FollowUp      string
	FollowUpDelay time.Duration
}

// flipperCommands returns the menu registry. Order is preserved as menu
// order; grouping is rendered as section headers in the menu component.
func flipperCommands() []FlipperCommand {
	cmds := []FlipperCommand{
		{
			ID:     "device_info",
			Label:  "Device info",
			Group:  "Device",
			Cmd:    "device_info",
			Format: formatKVResponse(deviceInfoFieldOrder, deviceInfoLabels),
		},
		{
			ID:     "info_power",
			Label:  "Power & battery",
			Group:  "Device",
			Cmd:    "info power",
			Format: formatKVResponse(powerFieldOrder, powerLabels),
		},
	}
	cmds = append(cmds, systemCommands()...)
	cmds = append(cmds, hardwareCommands()...)
	return cmds
}

// findCommand returns the command with the given id, or nil.
func findCommand(id string) *FlipperCommand {
	for _, c := range flipperCommands() {
		if c.ID == id {
			return &c
		}
	}
	return nil
}

// deviceInfoFieldOrder lists the most useful device_info fields first; the
// remainder are appended in alphabetical order so we never silently drop data.
var deviceInfoFieldOrder = []string{
	"firmware_version",
	"hardware_ver",
	"hardware_model",
	"hardware_target",
	"firmware_build_date",
	"firmware_commit",
	"firmware_branch",
	"radio_stack",
	"radio_alive",
	"radio_fus_major",
	"radio_fus_minor",
	"radio_stack_major",
	"radio_stack_minor",
}

// deviceInfoLabels overrides the auto-prettified label for keys that don't
// auto-format into a clean label (e.g. "Hardware Ver" → "Hardware version").
var deviceInfoLabels = map[string]string{
	"firmware_version":    "Firmware version",
	"hardware_ver":        "Hardware version",
	"hardware_model":      "Hardware model",
	"hardware_target":     "Hardware target",
	"firmware_build_date": "Firmware build",
	"firmware_commit":     "Firmware commit",
	"firmware_branch":     "Firmware branch",
	"radio_stack":         "Radio stack",
	"radio_alive":         "Radio alive",
}

// powerFieldOrder lists "info power" fields in a useful order (battery state
// first, then voltages and currents, then health/temperature).
var powerFieldOrder = []string{
	"charge.level",
	"charge.voltage",
	"charge.current",
	"charge.temperature",
	"battery.voltage",
	"battery.current",
	"battery.temperature",
	"battery.health",
	"battery.cycles",
	"battery.capacity_remaining",
	"battery.capacity_full",
	"battery.capacity_design",
	"system.voltage",
	"system.current",
	"vbus.voltage",
	"vbus.current",
}

var powerLabels = map[string]string{
	"charge.level":               "Charge",
	"charge.voltage":             "Charge voltage",
	"charge.current":             "Charge current",
	"charge.temperature":         "Charge temperature",
	"battery.voltage":            "Battery voltage",
	"battery.current":            "Battery current",
	"battery.temperature":        "Battery temperature",
	"battery.health":             "Battery health",
	"battery.cycles":             "Battery cycles",
	"battery.capacity_remaining": "Capacity remaining",
	"battery.capacity_full":      "Capacity full",
	"battery.capacity_design":    "Capacity design",
	"system.voltage":             "System voltage",
	"system.current":             "System current",
	"vbus.voltage":               "VBUS voltage",
	"vbus.current":               "VBUS current",
}

// formatKVResponse returns a Format function that parses the raw response as
// Flipper "key : value" lines and renders a label/value table.
//
// The preferred field order appears first; remaining keys are appended in
// alphabetical order so unknown / new firmware fields surface instead of
// being dropped.
func formatKVResponse(order []string, labels map[string]string) func([]byte, *tui.TerminalTheme) string {
	return func(raw []byte, t *tui.TerminalTheme) string {
		fields := parseFlipperKV(raw)
		if len(fields) == 0 {
			return t.Dim.Render("  (no fields parsed from response)")
		}

		// Preserve preferred order; track what we've emitted so the alpha tail
		// doesn't repeat them.
		seen := make(map[string]bool, len(fields))
		var keys []string
		for _, k := range order {
			if _, ok := fields[k]; ok {
				keys = append(keys, k)
				seen[k] = true
			}
		}
		var rest []string
		for k := range fields {
			if !seen[k] {
				rest = append(rest, k)
			}
		}
		sort.Strings(rest)
		keys = append(keys, rest...)

		// Compute label-column width for alignment.
		maxLabel := 0
		display := make([]string, len(keys))
		for i, k := range keys {
			lbl := labels[k]
			if lbl == "" {
				lbl = prettyLabel(k)
			}
			display[i] = lbl
			if n := len(lbl); n > maxLabel {
				maxLabel = n
			}
		}

		var b strings.Builder
		for i, k := range keys {
			lbl := display[i]
			pad := strings.Repeat(" ", maxLabel-len(lbl))
			b.WriteString("  ")
			b.WriteString(t.HeaderKey.Render(lbl + pad))
			b.WriteString("  ")
			b.WriteString(t.HeaderVal.Render(fields[k]))
			b.WriteString("\n")
		}
		return b.String()
	}
}
