package flipper

import (
	"strings"

	"github.com/olivierpoupier/patch/tui"
)

// knownCommands is the set of top-level Flipper CLI command names PATCH
// suggests in terminal mode. Sourced from the user's actual firmware `help`
// listing (mix of built-ins from cli_main_commands plus plugins loaded from
// /ext/apps_data/cli/plugins). Sorted alphabetically because the suggestions
// popup renders them in this order.
var knownCommands = []string{
	"!",
	"?",
	"bt",
	"buzzer",
	"clear",
	"crypto",
	"date",
	"device_info",
	"echo",
	"exit",
	"factory_reset",
	"free",
	"free_blocks",
	"gpio",
	"hello_world",
	"help",
	"i2c",
	"ikey",
	"info",
	"input",
	"ir",
	"js",
	"led",
	"loader",
	"log",
	"neofetch",
	"nfc",
	"onewire",
	"power",
	"reload_ext_cmds",
	"rfid",
	"sleep",
	"src",
	"start_rpc_session",
	"storage",
	"subghz",
	"subshell_demo",
	"sysctl",
	"top",
	"update",
	"uptime",
	"vibro",
}

// maxSuggestions caps the popup to a single readable line. Beyond this we
// show a "+N more" tail so the user knows there are additional matches.
const maxSuggestions = 8

// commandSuggestions returns the list of known commands whose names start
// with prefix. Returns nil when the user is past the first token (we don't
// suggest subcommands or arguments yet) or when the prefix is empty.
func commandSuggestions(input string) []string {
	// Only suggest while the user is still typing the first token. Once a
	// space appears, they're entering arguments — leave that to the device.
	if strings.ContainsRune(input, ' ') {
		return nil
	}
	if input == "" {
		return nil
	}
	var matches []string
	for _, c := range knownCommands {
		if strings.HasPrefix(c, input) {
			matches = append(matches, c)
		}
	}
	return matches
}

// commonPrefix returns the longest string that is a prefix of every input.
// Used by Tab-completion to advance the user's input as far as the
// unambiguous portion of the matches goes (bash-style behavior).
func commonPrefix(strs []string) string {
	if len(strs) == 0 {
		return ""
	}
	prefix := strs[0]
	for _, s := range strs[1:] {
		for !strings.HasPrefix(s, prefix) && len(prefix) > 0 {
			prefix = prefix[:len(prefix)-1]
		}
		if prefix == "" {
			return ""
		}
	}
	return prefix
}

// renderSuggestionsPopup produces a one-line popup like
//
//	▸ uptime  unmount  …  +2 more
//
// or returns "" when there's nothing to show. The matched-prefix portion of
// each suggestion is dimmed so the typed-in portion is visually distinct
// from what Tab would auto-complete.
func renderSuggestionsPopup(input string, t *tui.TerminalTheme) string {
	matches := commandSuggestions(input)
	if len(matches) == 0 {
		return ""
	}
	var parts []string
	limit := len(matches)
	if limit > maxSuggestions {
		limit = maxSuggestions
	}
	for i := 0; i < limit; i++ {
		c := matches[i]
		// Split into typed prefix and the rest so we can dim the prefix and
		// emphasize the suffix that Tab would insert.
		typed := input
		rest := strings.TrimPrefix(c, input)
		parts = append(parts, t.Dim.Render(typed)+t.HeaderVal.Render(rest))
	}
	tail := ""
	if len(matches) > maxSuggestions {
		tail = "  " + t.Dim.Render("…")
	}
	label := t.HeaderKey.Render("▸ ")
	return "  " + label + strings.Join(parts, t.Dim.Render("  ")) + tail
}

// trimLastWord drops the trailing whitespace-delimited word from s. Used to
// mirror the device's ctrl+w behavior in the local input buffer.
func trimLastWord(s string) string {
	s = strings.TrimRight(s, " ")
	if i := strings.LastIndexByte(s, ' '); i >= 0 {
		return s[:i+1]
	}
	return ""
}
