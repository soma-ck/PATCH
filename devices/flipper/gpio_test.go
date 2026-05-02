package flipper

import (
	"testing"
)

func TestGPIOModelInitializesAllPins(t *testing.T) {
	g := newGPIOModel()
	for _, pin := range gpioPins {
		if g.pins[pin] == nil {
			t.Errorf("pin %q not initialised", pin)
		}
	}
	if g.cursor != 0 {
		t.Errorf("initial cursor = %d, want 0", g.cursor)
	}
}

func TestGPIOMoveBoundsAndGrid(t *testing.T) {
	// Layout (cursor 0..7 mapping):
	//   0 PA7   1 PA6
	//   2 PA4   3 PB3
	//   4 PB2   5 PC3
	//   6 PC1   7 PC0

	cases := []struct {
		name     string
		ops      func(*gpioModel)
		wantPin  string
	}{
		{"start", func(g *gpioModel) {}, "PA7"},
		{"right once", func(g *gpioModel) { g.move(1, 0) }, "PA6"},
		{"right twice clamps", func(g *gpioModel) { g.move(1, 0); g.move(1, 0) }, "PA6"},
		{"left from col 0 stays", func(g *gpioModel) { g.move(-1, 0) }, "PA7"},
		{"down from PA7 → PA4", func(g *gpioModel) { g.move(0, 1) }, "PA4"},
		{"down four rows then right", func(g *gpioModel) {
			g.move(0, 1)
			g.move(0, 1)
			g.move(0, 1)
			g.move(1, 0)
		}, "PC0"},
		{"down from bottom row stays", func(g *gpioModel) {
			g.move(0, 1)
			g.move(0, 1)
			g.move(0, 1)
			g.move(0, 1)
		}, "PC1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := newGPIOModel()
			c.ops(&g)
			if got := g.selectedPin(); got != c.wantPin {
				t.Errorf("selected pin: got %q, want %q (cursor=%d)", got, c.wantPin, g.cursor)
			}
		})
	}
}

func TestParseGPIOReadResponse(t *testing.T) {
	cases := []struct {
		name string
		raw  []byte
		want string
	}{
		{
			"bare digit",
			[]byte(">: gpio read PA4\r\n0\r\n>: "),
			"0",
		},
		{
			"bare digit one",
			[]byte(">: gpio read PA4\r\n1\r\n>: "),
			"1",
		},
		{
			"verbose form",
			[]byte(">: gpio read PA4\r\nPA4 = 1\r\n>: "),
			"1",
		},
		{
			"unparseable",
			[]byte(">: gpio read PA4\r\nUsage: gpio <cmd> <args>\r\n>: "),
			"",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := parseGPIOReadResponse(c.raw); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestGPIOReadCmdIDRoundTrip(t *testing.T) {
	id := gpioReadCmdID("PA4")
	if got := pinFromGPIOReadID(id); got != "PA4" {
		t.Errorf("round-trip: got %q, want %q", got, "PA4")
	}
	if got := pinFromGPIOReadID("device_info"); got != "" {
		t.Errorf("non-gpio id should yield empty pin, got %q", got)
	}
}

func TestApplyGPIOReadResultUpdatesPinState(t *testing.T) {
	theme := newGPIOModel()
	m := &Model{gpio: theme}
	consumed := m.applyGPIOReadResult(commandResultMsg{
		ID:  gpioReadCmdID("PA4"),
		Raw: []byte(">: gpio read PA4\r\n1\r\n>: "),
	})
	if !consumed {
		t.Fatal("gpio result should be consumed")
	}
	st := m.gpio.pins["PA4"]
	if !st.HasValue || st.LastValue != "1" {
		t.Errorf("PA4 state: HasValue=%v LastValue=%q, want HasValue=true LastValue=\"1\"", st.HasValue, st.LastValue)
	}
	if st.Reading {
		t.Error("Reading should be cleared after result arrives")
	}
}

func TestApplyGPIOReadResultErrorBranch(t *testing.T) {
	m := &Model{gpio: newGPIOModel()}
	m.applyGPIOReadResult(commandResultMsg{
		ID:  gpioReadCmdID("PA4"),
		Err: errCommandTimeout{cmd: gpioReadCmdID("PA4")},
	})
	st := m.gpio.pins["PA4"]
	if st.LastErr == "" {
		t.Error("expected LastErr to be set on timeout")
	}
	if st.HasValue {
		t.Error("HasValue should remain false on error")
	}
}

func TestApplyGPIOReadResultIgnoresNonGPIOResult(t *testing.T) {
	m := &Model{gpio: newGPIOModel()}
	if m.applyGPIOReadResult(commandResultMsg{ID: "device_info"}) {
		t.Error("non-gpio result must not be consumed")
	}
}

func TestGPIOMenuEntryPresent(t *testing.T) {
	items := menuItems()
	for _, it := range items {
		if it.ID == gpioMenuID {
			if it.Group != "Hardware" {
				t.Errorf("GPIO menu entry should be in Hardware group, got %q", it.Group)
			}
			return
		}
	}
	t.Error("gpio_open entry missing from menuItems()")
}
