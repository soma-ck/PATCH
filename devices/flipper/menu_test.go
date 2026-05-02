package flipper

import (
	"testing"

	"github.com/olivierpoupier/patch/tui"
	"github.com/olivierpoupier/patch/tui/components"
)

func TestMenuItemsFromCommandsPreservesOrderAndGroups(t *testing.T) {
	cmds := flipperCommands()
	items := menuItemsFromCommands(cmds)
	if len(items) != len(cmds) {
		t.Fatalf("len mismatch: %d vs %d", len(items), len(cmds))
	}
	for i := range cmds {
		if items[i].ID != cmds[i].ID {
			t.Errorf("[%d] ID: got %q, want %q", i, items[i].ID, cmds[i].ID)
		}
		if items[i].Group != cmds[i].Group {
			t.Errorf("[%d] Group: got %q, want %q", i, items[i].Group, cmds[i].Group)
		}
	}
}

func TestMenuListCursorClampsToBounds(t *testing.T) {
	theme := tui.NewTheme().Terminal
	items := menuItemsFromCommands(flipperCommands())
	m := components.NewMenuList(theme, items)

	if m.Cursor() != 0 {
		t.Fatalf("initial cursor: got %d, want 0", m.Cursor())
	}
	m = m.CursorUp() // already at top, stays at 0
	if m.Cursor() != 0 {
		t.Fatalf("CursorUp at top: got %d, want 0", m.Cursor())
	}
	for range len(items) + 5 {
		m = m.CursorDown()
	}
	if got := m.Cursor(); got != len(items)-1 {
		t.Fatalf("CursorDown past bottom: got %d, want %d", got, len(items)-1)
	}
}

func TestMenuListSelectedReturnsCurrentItem(t *testing.T) {
	theme := tui.NewTheme().Terminal
	items := menuItemsFromCommands(flipperCommands())
	m := components.NewMenuList(theme, items)
	sel := m.Selected()
	if sel == nil {
		t.Fatal("expected non-nil selection on first item")
	}
	if sel.ID != items[0].ID {
		t.Errorf("selected ID: got %q, want %q", sel.ID, items[0].ID)
	}
	m = m.CursorDown()
	sel = m.Selected()
	if sel.ID != items[1].ID {
		t.Errorf("after down: got %q, want %q", sel.ID, items[1].ID)
	}
}

func TestFindCommandReturnsNilForUnknown(t *testing.T) {
	if c := findCommand("nope"); c != nil {
		t.Fatalf("findCommand(nope): got %+v, want nil", c)
	}
	if c := findCommand("device_info"); c == nil {
		t.Fatal("findCommand(device_info): unexpected nil")
	}
}

func TestFormatKVResponseRendersFields(t *testing.T) {
	theme := tui.NewTheme().Terminal
	cmd := findCommand("device_info")
	if cmd == nil {
		t.Fatal("device_info missing from registry")
	}
	raw := []byte("hardware_ver        : 13\r\nfirmware_version    : 0.95.1\r\n>: ")
	out := cmd.Format(raw, theme)
	for _, want := range []string{"Firmware version", "0.95.1", "Hardware version", "13"} {
		if !contains(out, want) {
			t.Errorf("formatted output missing %q\nfull output:\n%s", want, out)
		}
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || (len(sub) > 0 && indexOf(s, sub) >= 0))
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
