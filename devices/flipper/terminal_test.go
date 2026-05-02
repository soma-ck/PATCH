package flipper

import (
	"reflect"
	"strings"
	"testing"

	"github.com/olivierpoupier/patch/tui"
)

func TestCommandSuggestionsPrefixMatch(t *testing.T) {
	cases := []struct {
		input string
		want  []string
	}{
		{"u", []string{"update", "uptime"}},
		{"f", []string{"factory_reset", "free", "free_blocks"}},
		{"info", []string{"info"}},
		{"d", []string{"date", "device_info"}},
		{"xyz", nil},
		{"", nil},
		{"led r", nil}, // past first token → no suggestions
	}
	for _, c := range cases {
		t.Run(c.input, func(t *testing.T) {
			got := commandSuggestions(c.input)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("commandSuggestions(%q) = %v, want %v", c.input, got, c.want)
			}
		})
	}
}

func TestCommonPrefix(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{[]string{"free", "free_blocks"}, "free"},
		{[]string{"led", "log"}, "l"},
		{[]string{"uptime"}, "uptime"},
		{nil, ""},
		{[]string{"abc", "xyz"}, ""},
	}
	for _, c := range cases {
		got := commonPrefix(c.in)
		if got != c.want {
			t.Errorf("commonPrefix(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestRenderSuggestionsPopupShape(t *testing.T) {
	theme := tui.NewTheme().Terminal
	out := renderSuggestionsPopup("u", theme)
	if out == "" {
		t.Fatal("expected popup for 'u', got empty")
	}
	// The popup splits each match into two styled segments (typed prefix
	// dimmed, rest emphasised) so the rendered bytes interleave SGR codes
	// between letters. Strip SGR before checking.
	if !strings.Contains(stripSGR(out), "uptime") {
		t.Errorf("popup should contain 'uptime', got:\n%s", out)
	}
}

func TestRenderSuggestionsPopupEmptyOnNoMatch(t *testing.T) {
	theme := tui.NewTheme().Terminal
	if got := renderSuggestionsPopup("zzzz", theme); got != "" {
		t.Errorf("no-match should yield empty popup, got %q", got)
	}
	if got := renderSuggestionsPopup("", theme); got != "" {
		t.Errorf("empty input should yield empty popup, got %q", got)
	}
	if got := renderSuggestionsPopup("led 123", theme); got != "" {
		t.Errorf("past-first-token should yield empty popup, got %q", got)
	}
}

func TestTrimLastWord(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"led r 255", "led r "},
		{"led ", ""},
		{"led", ""},
		{"", ""},
		{"  led  r  ", "  led  "},
	}
	for _, c := range cases {
		if got := trimLastWord(c.in); got != c.want {
			t.Errorf("trimLastWord(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestUpdateTerminalInputTracksPrintable(t *testing.T) {
	m := &Model{}
	cases := []struct {
		key  string
		data []byte
		want string
	}{
		{"u", []byte("u"), "u"},
		{"p", []byte("p"), "up"},
		{"backspace", []byte{0x7f}, "u"},
		{"t", []byte("t"), "ut"},
		{"i", []byte("i"), "uti"},
		{"ctrl+u", []byte{0x15}, ""},
		{"v", []byte("v"), "v"},
		{"i", []byte("i"), "vi"},
		{"b", []byte("b"), "vib"},
		{"enter", []byte{'\r'}, ""},
	}
	for _, c := range cases {
		m.updateTerminalInput(c.data, c.key)
		if m.terminalInput != c.want {
			t.Errorf("after key %q: terminalInput = %q, want %q", c.key, m.terminalInput, c.want)
		}
	}
}

func TestUpdateTerminalInputCtrlW(t *testing.T) {
	m := &Model{terminalInput: "led r 255"}
	m.updateTerminalInput([]byte{0x17}, "ctrl+w")
	if m.terminalInput != "led r " {
		t.Errorf("ctrl+w: got %q, want %q", m.terminalInput, "led r ")
	}
	m.updateTerminalInput([]byte{0x17}, "ctrl+w")
	if m.terminalInput != "led " {
		t.Errorf("ctrl+w (again): got %q, want %q", m.terminalInput, "led ")
	}
}

func TestKnownCommandsCoreEntriesPresent(t *testing.T) {
	required := []string{"info", "uptime", "free", "led", "gpio", "i2c", "vibro"}
	have := make(map[string]bool, len(knownCommands))
	for _, c := range knownCommands {
		have[c] = true
	}
	for _, r := range required {
		if !have[r] {
			t.Errorf("required command %q missing from knownCommands", r)
		}
	}
}
