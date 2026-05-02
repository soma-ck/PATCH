package flipper

import (
	"strings"
	"testing"

	"github.com/olivierpoupier/patch/tui"
)

func TestSubsystemCommandsRegistered(t *testing.T) {
	if findCommand("i2c_scan") == nil {
		t.Error("i2c_scan not registered")
	}
}

func TestSubsystemEntriesAreInSubsystemsGroup(t *testing.T) {
	c := findCommand("i2c_scan")
	if c == nil {
		t.Fatal("i2c_scan missing from registry")
	}
	if c.Group != "Subsystems" {
		t.Errorf("i2c_scan.Group = %q, want %q", c.Group, "Subsystems")
	}
}

func TestInteractivePluginsNotRegistered(t *testing.T) {
	// The subghz/nfc/ir bare commands enter interactive plugin mode on stock
	// firmware, leaving the device stuck until ETX. They were briefly
	// registered as "info" entries assuming bare-invocation prints usage —
	// it doesn't. Guard against re-introducing them as one-shot menu items.
	for _, id := range []string{"subghz_help", "nfc_help", "ir_help"} {
		if findCommand(id) != nil {
			t.Errorf("interactive-plugin entry %q must not be registered as a one-shot command", id)
		}
	}
}

func TestParseI2CScanFindsAddresses(t *testing.T) {
	// Realistic firmware output: a card responding at 0x68 (DS3231 RTC) and
	// 0x76 (BMP280-style sensor). Note: addresses are at row.col positions
	// (0x68 = row 6, col 8; 0x76 = row 7, col 6).
	raw := []byte("" +
		">: i2c\r\n" +
		"Scanning external i2c on PC0(SCL)/PC1(SDA)\r\n" +
		"Clock: 100khz, 7bit address\r\n" +
		"\r\n" +
		"   | 0 1 2 3 4 5 6 7 8 9 A B C D E F\r\n" +
		"--+--------------------------------\r\n" +
		"0 | - - - - - - - - - - - - - - - -\r\n" +
		"1 | - - - - - - - - - - - - - - - -\r\n" +
		"2 | - - - - - - - - - - - - - - - -\r\n" +
		"3 | - - - - - - - - - - - - - - - -\r\n" +
		"4 | - - - - - - - - - - - - - - - -\r\n" +
		"5 | - - - - - - - - - - - - - - - -\r\n" +
		"6 | - - - - - - - - # - - - - - - -\r\n" +
		"7 | - - - - - - # - - - - - - - - -\r\n" +
		">: ")
	got := parseI2CScan(raw)
	want := []byte{0x68, 0x76}
	if len(got) != len(want) {
		t.Fatalf("found %d addresses, want %d (got %v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("addr[%d] = 0x%02X, want 0x%02X", i, got[i], want[i])
		}
	}
}

func TestParseI2CScanEmptyBus(t *testing.T) {
	raw := []byte("" +
		">: i2c\r\n" +
		"   | 0 1 2 3 4 5 6 7 8 9 A B C D E F\r\n" +
		"0 | - - - - - - - - - - - - - - - -\r\n" +
		"1 | - - - - - - - - - - - - - - - -\r\n" +
		">: ")
	if got := parseI2CScan(raw); len(got) != 0 {
		t.Errorf("expected no addresses on empty bus, got %v", got)
	}
}

func TestParseI2CScanIgnoresColumnHeader(t *testing.T) {
	// The column header "| 0 1 2 ..." has no row-digit prefix and must not
	// be misread as row 0.
	raw := []byte("" +
		"   | 0 1 2 3 4 5 6 7 8 9 A B C D E F\r\n" +
		"0 | # - - - - - - - - - - - - - - -\r\n" +
		">: ")
	got := parseI2CScan(raw)
	if len(got) != 1 || got[0] != 0x00 {
		t.Errorf("expected single 0x00, got %v", got)
	}
}

func TestFormatI2CScanRendersAddressesAndRawGrid(t *testing.T) {
	theme := tui.NewTheme().Terminal
	raw := []byte("" +
		">: i2c\r\n" +
		"Scanning external i2c on PC0(SCL)/PC1(SDA)\r\n" +
		"   | 0 1 2 3 4 5 6 7 8 9 A B C D E F\r\n" +
		"6 | - - - - - - - - # - - - - - - -\r\n" +
		">: ")
	out := formatI2CScan(raw, theme)
	if !strings.Contains(out, "0x68") {
		t.Errorf("expected formatted address 0x68 in output:\n%s", out)
	}
	if !strings.Contains(out, "Raw firmware output") {
		t.Errorf("expected raw-grid section in output:\n%s", out)
	}
	if !strings.Contains(out, "Scanning external") {
		t.Errorf("expected raw firmware text retained:\n%s", out)
	}
}

func TestFormatI2CScanShowsNoDevicesMessage(t *testing.T) {
	theme := tui.NewTheme().Terminal
	raw := []byte("" +
		">: i2c\r\n" +
		"   | 0 1 2 3 4 5 6 7 8 9 A B C D E F\r\n" +
		"0 | - - - - - - - - - - - - - - - -\r\n" +
		">: ")
	out := formatI2CScan(raw, theme)
	if !strings.Contains(out, "No devices responded") {
		t.Errorf("empty bus should show 'No devices responded':\n%s", out)
	}
}

func TestParseHexNibble(t *testing.T) {
	cases := []struct {
		in   byte
		val  byte
		ok   bool
	}{
		{'0', 0, true},
		{'9', 9, true},
		{'a', 10, true},
		{'F', 15, true},
		{'g', 0, false},
		{':', 0, false},
	}
	for _, c := range cases {
		v, ok := parseHexNibble(c.in)
		if ok != c.ok || (ok && v != c.val) {
			t.Errorf("parseHexNibble(%q) = (%d, %v), want (%d, %v)", c.in, v, ok, c.val, c.ok)
		}
	}
}
