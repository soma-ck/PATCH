package flipper

import (
	"fmt"
	"runtime"
	"strings"
	"time"

	"github.com/olivierpoupier/patch/serialterm"
	"github.com/olivierpoupier/patch/tui"
	"github.com/olivierpoupier/patch/tui/components"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// captureDuration is the window during which RX bytes are mirrored into
// captureBuf after sending the header-query commands. Flipper's device_info
// and "info power" responses reliably arrive within a few hundred
// milliseconds; the buffer gives a generous margin.
const captureDuration = 900 * time.Millisecond

// headerFieldOrder controls which parsed fields appear in the header, and in
// what order.
var headerFieldOrder = []string{"firmware_version", "hardware_ver", "charge.level"}

// Model is the Flipper Zero device view. It composes a serialterm.Session for
// I/O, a serialterm.Scrollback for line buffering, and adds Flipper-specific
// behaviour on top: on-open diagnostic queries, "key : value" parsing, and
// battery-percent formatting.
type Model struct {
	theme  *tui.Theme
	device tui.SerialDeviceInfo

	session    *serialterm.Session
	scrollback *serialterm.Scrollback
	scroll     components.ScrollableView

	headerFields map[string]string
	capturing    bool
	captureBuf   []byte

	err          error
	disconnected bool

	width, height int
}

// New constructs an empty Flipper view. Call Open to attach a device.
func New(theme *tui.Theme) *Model {
	return &Model{
		theme:        theme,
		session:      &serialterm.Session{},
		scrollback:   serialterm.NewScrollback(0),
		scroll:       components.NewScrollableView(theme),
		headerFields: make(map[string]string),
	}
}

// Name returns a short identifier for logs and error messages.
func (m *Model) Name() string { return "Flipper Zero" }

// Open activates the view for the given device and kicks off the serial open.
func (m *Model) Open(info tui.SerialDeviceInfo) tea.Cmd {
	m.device = info
	if m.device.Baud == 0 {
		m.device.Baud = 115200
	}
	if m.device.Name == "" {
		m.device.Name = "Flipper Zero"
	}
	m.scrollback.Clear()
	m.headerFields = make(map[string]string)
	m.captureBuf = m.captureBuf[:0]
	m.capturing = false
	m.err = nil
	m.disconnected = false
	return serialterm.OpenSerialSession(m.device.PortPath, m.device.Baud)
}

// Close stops the I/O session.
func (m *Model) Close() {
	m.session.Close()
	m.err = nil
	m.disconnected = false
}

// SetSize stores the full-screen dimensions.
func (m *Model) SetSize(w, h int) {
	m.width = w
	m.height = h
}

// Update routes messages to the relevant handler.
func (m *Model) Update(msg tea.Msg) (tui.DeviceView, tea.Cmd) {
	switch msg := msg.(type) {
	case serialterm.OpenedMsg:
		return m.handleOpened(msg)
	case serialterm.OpenErrMsg:
		m.err = msg.Err
		return m, nil
	case serialterm.RxMsg:
		return m.handleRx(msg)
	case serialterm.RxClosedMsg:
		return m.handleRxClosed(msg)
	case serialterm.TxDoneMsg:
		if msg.Err != nil {
			m.err = msg.Err
		}
		return m, nil
	case captureEndMsg:
		return m.handleCaptureEnd()
	case tea.KeyPressMsg:
		return m.handleKeyPress(msg)
	}
	return m, nil
}

func (m *Model) handleOpened(msg serialterm.OpenedMsg) (tui.DeviceView, tea.Cmd) {
	m.session.Install(msg)
	m.err = nil
	cmds := []tea.Cmd{m.session.WaitRx()}
	if c := m.startHeaderCapture(); c != nil {
		cmds = append(cmds, c)
	}
	return m, tea.Batch(cmds...)
}

func (m *Model) handleRx(msg serialterm.RxMsg) (tui.DeviceView, tea.Cmd) {
	// While capturing, RX belongs to our diagnostic queries — route it to the
	// parse buffer only so the user's scrollback stays clean (and so a device
	// that rejects a query doesn't leak "unknown command" errors on open).
	if m.capturing {
		m.captureBuf = append(m.captureBuf, msg.Data...)
	} else {
		m.scrollback.Write(msg.Data)
	}
	return m, m.session.WaitRx()
}

func (m *Model) handleRxClosed(msg serialterm.RxClosedMsg) (tui.DeviceView, tea.Cmd) {
	m.disconnected = true
	if msg.Err != nil {
		m.err = msg.Err
	}
	m.session.MarkDisconnected()
	return m, nil
}

func (m *Model) handleCaptureEnd() (tui.DeviceView, tea.Cmd) {
	m.capturing = false
	if len(m.captureBuf) > 0 {
		for k, v := range parseFlipperKV(m.captureBuf) {
			m.headerFields[k] = v
		}
	}
	m.captureBuf = m.captureBuf[:0]
	// The Flipper printed its post-response prompt during the capture
	// window, so it never reached the scrollback. Send a bare \r to elicit
	// a fresh prompt now that RX flows to the user view, sparing them the
	// "press enter to wake it up" gesture on every connect.
	if m.session.Active() && !m.disconnected {
		return m, m.session.Send([]byte("\r"))
	}
	return m, nil
}

func (m *Model) handleKeyPress(k tea.KeyPressMsg) (tui.DeviceView, tea.Cmd) {
	switch k.String() {
	case "esc":
		return m, func() tea.Msg { return tui.CloseDeviceMsg{} }
	case "ctrl+l":
		m.scrollback.Clear()
		if m.session.Active() && !m.disconnected {
			return m, m.session.Send([]byte("\r"))
		}
		return m, nil
	case "ctrl+r":
		return m, m.startHeaderCapture()
	}
	if !m.session.Active() || m.disconnected {
		return m, nil
	}
	data := serialterm.KeyToBytes(k)
	if len(data) == 0 {
		return m, nil
	}
	return m, m.session.Send(data)
}

// startHeaderCapture sends the Flipper-specific diagnostic queries and opens
// a capture window during which incoming bytes populate captureBuf instead of
// the scrollback. Returns nil if the session is not yet live.
//
// device_info is a legacy alias that outputs underscore-joined keys
// (firmware_version, hardware_ver, ...). Power info is only available as the
// modern "info power" subcommand, which uses dot-joined keys (charge.level,
// battery.voltage, ...). See furi_hal_power_info_get in the flipperzero
// firmware source.
func (m *Model) startHeaderCapture() tea.Cmd {
	if !m.session.Active() {
		return nil
	}
	cmds := []tea.Cmd{
		m.session.Send([]byte("device_info\r\n")),
		m.session.Send([]byte("info power\r\n")),
	}
	m.capturing = true
	m.captureBuf = m.captureBuf[:0]
	cmds = append(cmds, captureWindowCmd(captureDuration))
	return tea.Batch(cmds...)
}

// captureEndMsg fires after the diagnostic-query capture window expires.
type captureEndMsg struct{}

func captureWindowCmd(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return captureEndMsg{} })
}

// View renders the full-screen modal.
func (m *Model) View(width, height int) string {
	m.width = width
	m.height = height
	if width <= 0 || height <= 0 {
		return ""
	}

	t := m.theme.Terminal

	header := m.renderHeader(width)
	footer := m.renderFooter(width)

	headerH := lipgloss.Height(header)
	footerH := lipgloss.Height(footer)
	bodyHeight := height - headerH - footerH - 1
	if bodyHeight < 3 {
		bodyHeight = 3
	}
	bodyWidth := width - 1

	body := m.renderBody(bodyWidth)

	m.scroll = m.scroll.SetSize(bodyWidth, bodyHeight)
	m.scroll = m.scroll.SetContent(body)

	// Pin scroll to the bottom so latest output is always visible.
	lines := strings.Count(body, "\n")
	if lines > bodyHeight {
		m.scroll = m.scroll.ScrollToLine(lines)
	}

	screen := t.Body.Render(m.scroll.View())
	return header + "\n" + screen + "\n" + footer
}

func (m *Model) renderHeader(width int) string {
	t := m.theme.Terminal
	sep := t.Separator.Render(strings.Repeat("═", width))

	bannerLine := t.Banner.Render(banner)

	name := m.device.Name
	if name == "" {
		name = "Flipper Zero"
	}

	vid := strings.TrimPrefix(strings.ToLower(m.device.VID), "0x")
	pid := strings.TrimPrefix(strings.ToLower(m.device.PID), "0x")
	vidPid := vid
	if pid != "" {
		vidPid = vid + ":" + pid
	}

	rows := []string{
		components.FieldLine(t,
			components.KV{Label: "Device", Value: name},
			components.KV{Label: "Port", Value: m.device.PortPath},
			components.KV{Label: "Baud", Value: fmt.Sprintf("%d", m.device.Baud)},
		),
		components.FieldLine(t,
			components.KV{Label: "VID:PID", Value: vidPid},
			components.KV{Label: "Serial", Value: components.OrDash(m.device.SerialNumber)},
		),
	}

	if len(m.headerFields) > 0 {
		var fs []components.KV
		for _, key := range headerFieldOrder {
			val, ok := m.headerFields[key]
			if !ok || val == "" {
				continue
			}
			if key == "charge.level" {
				val += "%"
			}
			fs = append(fs, components.KV{Label: prettyLabel(key), Value: val})
		}
		if len(fs) > 0 {
			rows = append(rows, components.FieldLine(t, fs...))
		}
	}

	if m.disconnected {
		rows = append(rows, t.DisconnectBanner.Render(" DEVICE DISCONNECTED "))
	}

	parts := []string{
		sep,
		components.Indent(bannerLine, 2),
		sep,
		components.Indent(strings.Join(rows, "\n"), 1),
		sep,
	}
	return strings.Join(parts, "\n")
}

func (m *Model) renderBody(width int) string {
	t := m.theme.Terminal

	if m.err != nil && !m.session.Active() {
		return m.renderOpenError(width)
	}
	if !m.session.Active() {
		return t.Dim.Render("  Opening serial port…")
	}
	lines := m.scrollback.Lines()
	if len(lines) == 0 {
		return t.Dim.Render("  (waiting for output — type to send bytes)")
	}
	var b strings.Builder
	for _, line := range lines {
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}

func (m *Model) renderOpenError(_ int) string {
	t := m.theme.Terminal

	var b strings.Builder
	b.WriteString(t.DisconnectBanner.Render(" FAILED TO OPEN PORT "))
	b.WriteString("\n\n")
	b.WriteString(t.Body.Render("  " + m.err.Error()))
	b.WriteString("\n\n")

	if serialterm.IsPermissionDenied(m.err.Error()) && runtime.GOOS == "linux" {
		b.WriteString(t.HeaderKey.Render("  This user cannot access the serial port."))
		b.WriteString("\n")
		b.WriteString(t.HeaderVal.Render("  Add yourself to the dialout group:"))
		b.WriteString("\n")
		b.WriteString(t.Body.Render("    sudo usermod -aG dialout $USER && newgrp dialout"))
		b.WriteString("\n")
		b.WriteString(t.Dim.Render("  (on Arch, the group is 'uucp')"))
		b.WriteString("\n")
	}
	return b.String()
}

func (m *Model) renderFooter(_ int) string {
	bindings := []components.KeyBinding{
		{Keys: "esc", Description: "close"},
		{Keys: "ctrl+l", Description: "clear"},
		{Keys: "ctrl+r", Description: "refresh info"},
		{Keys: "keys", Description: "→ device"},
	}
	var parts []string
	for _, b := range bindings {
		parts = append(parts, b.Keys+" "+b.Description)
	}
	return m.theme.Terminal.Help.Render("  " + strings.Join(parts, "  "))
}

// prettyLabel maps Flipper parsed keys to display labels. Unknown keys are
// capitalised from their underscore-joined form.
func prettyLabel(key string) string {
	switch key {
	case "firmware_version":
		return "Firmware"
	case "hardware_ver":
		return "Hardware"
	case "radio_stack":
		return "Radio"
	case "charge_level", "charge.level":
		return "Battery"
	}
	parts := strings.Split(key, "_")
	for i, p := range parts {
		if p == "" {
			continue
		}
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}
	return strings.Join(parts, " ")
}
