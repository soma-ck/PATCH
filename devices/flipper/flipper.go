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

// headerFieldOrder controls which parsed fields appear in the header, and in
// what order.
var headerFieldOrder = []string{"firmware_version", "hardware_ver", "charge.level"}

// viewMode is the current sub-view of the Flipper modal: a menu of curated
// commands, the raw serial terminal, or the detail pane showing a command's
// formatted result.
type viewMode int

const (
	modeMenu viewMode = iota
	modeTerminal
	modeDetail
)

// Model is the Flipper Zero device view. It composes a serialterm.Session for
// I/O, a serialterm.Scrollback for line buffering, and a menu-driven UI on
// top: the user picks a command from a list, the model issues the
// corresponding Flipper CLI command over serial, captures the response via
// commandRunner, and renders a parsed result in the detail pane.
type Model struct {
	theme  *tui.Theme
	device tui.SerialDeviceInfo

	session    *serialterm.Session
	scrollback *serialterm.Scrollback
	scroll     components.ScrollableView

	mode viewMode
	menu components.MenuList

	runner commandRunner
	// headerCaptureQueue holds the remaining raw CLI commands to run for
	// header capture. While non-empty (or the runner is busy with one of them
	// already issued), results are routed to header-field parsing instead of
	// the detail pane.
	headerCaptureQueue  []string
	headerCaptureActive bool

	// Detail pane state — populated when a menu command finishes.
	detailCmdID   string
	detailContent string
	detailErr     error
	detailRunning bool

	headerFields map[string]string

	err          error
	disconnected bool

	width, height int
}

// New constructs an empty Flipper view. Call Open to attach a device.
func New(theme *tui.Theme) *Model {
	m := &Model{
		theme:        theme,
		session:      &serialterm.Session{},
		scrollback:   serialterm.NewScrollback(0),
		scroll:       components.NewScrollableView(theme),
		headerFields: make(map[string]string),
		mode:         modeMenu,
	}
	m.menu = components.NewMenuList(theme.Terminal, menuItemsFromCommands(flipperCommands()))
	return m
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
	m.runner.reset()
	m.headerCaptureQueue = nil
	m.headerCaptureActive = false
	m.detailCmdID = ""
	m.detailContent = ""
	m.detailErr = nil
	m.detailRunning = false
	m.mode = modeMenu
	m.menu = m.menu.SetItems(menuItemsFromCommands(flipperCommands()))
	m.err = nil
	m.disconnected = false
	return serialterm.OpenSerialSession(m.device.PortPath, m.device.Baud)
}

// Close stops the I/O session.
func (m *Model) Close() {
	m.session.Close()
	m.runner.reset()
	m.headerCaptureQueue = nil
	m.headerCaptureActive = false
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
	case commandResultMsg:
		return m.handleCommandResult(msg)
	case commandTimeoutMsg:
		return m, m.runner.timeout(msg)
	case followUpMsg:
		return m.handleFollowUp(msg)
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
	cmds := []tea.Cmd{m.session.WaitRx()}
	// While a runner is in flight, RX belongs to that command — keep it out
	// of the user-visible scrollback so menu activity doesn't pollute the
	// terminal view.
	if m.runner.busy() {
		if c := m.runner.feed(msg.Data); c != nil {
			cmds = append(cmds, c)
		}
	} else {
		m.scrollback.Write(msg.Data)
	}
	return m, tea.Batch(cmds...)
}

func (m *Model) handleRxClosed(msg serialterm.RxClosedMsg) (tui.DeviceView, tea.Cmd) {
	m.disconnected = true
	if msg.Err != nil {
		m.err = msg.Err
	}
	m.session.MarkDisconnected()
	m.runner.reset()
	m.headerCaptureQueue = nil
	m.headerCaptureActive = false
	if m.detailRunning {
		m.detailRunning = false
		m.detailErr = fmt.Errorf("device disconnected")
	}
	return m, nil
}

func (m *Model) handleCommandResult(msg commandResultMsg) (tui.DeviceView, tea.Cmd) {
	if m.headerCaptureActive {
		if msg.Err == nil {
			for k, v := range parseFlipperKV(msg.Raw) {
				m.headerFields[k] = v
			}
		}
		m.headerCaptureActive = false
		return m, m.advanceHeaderCapture()
	}
	if msg.ID != m.detailCmdID {
		return m, nil
	}
	m.detailRunning = false
	m.detailErr = msg.Err
	if msg.Err != nil {
		m.detailContent = ""
		return m, nil
	}
	cmd := findCommand(msg.ID)
	if cmd == nil || cmd.Format == nil {
		m.detailContent = string(msg.Raw)
	} else {
		m.detailContent = cmd.Format(msg.Raw, m.theme.Terminal)
	}
	if cmd != nil && cmd.FollowUp != "" && cmd.FollowUpDelay > 0 {
		return m, scheduleFollowUp(cmd.FollowUp, cmd.FollowUpDelay)
	}
	return m, nil
}

// followUpMsg fires after a FlipperCommand with FollowUpDelay > 0 has had its
// primary response captured. The follow-up is sent fire-and-forget; any
// response from the device lands in the scrollback for terminal mode.
type followUpMsg struct {
	cmd string
}

func scheduleFollowUp(cmd string, delay time.Duration) tea.Cmd {
	return tea.Tick(delay, func(time.Time) tea.Msg {
		return followUpMsg{cmd: cmd}
	})
}

func (m *Model) handleFollowUp(msg followUpMsg) (tui.DeviceView, tea.Cmd) {
	if !m.session.Active() || m.disconnected {
		return m, nil
	}
	return m, m.session.Send([]byte(msg.cmd + "\r\n"))
}

func (m *Model) handleKeyPress(k tea.KeyPressMsg) (tui.DeviceView, tea.Cmd) {
	key := k.String()

	// Cross-mode shortcuts.
	switch key {
	case "ctrl+t":
		return m.toggleTerminal()
	case "ctrl+r":
		// Re-run header capture from any mode, but don't disturb any other
		// in-flight runner work.
		if !m.runner.busy() {
			return m, m.startHeaderCapture()
		}
		return m, nil
	}

	switch m.mode {
	case modeMenu:
		return m.handleMenuKey(k)
	case modeTerminal:
		return m.handleTerminalKey(k)
	case modeDetail:
		return m.handleDetailKey(k)
	}
	return m, nil
}

func (m *Model) handleMenuKey(k tea.KeyPressMsg) (tui.DeviceView, tea.Cmd) {
	switch k.String() {
	case "esc":
		return m, func() tea.Msg { return tui.CloseDeviceMsg{} }
	case "up", "k":
		m.menu = m.menu.CursorUp()
		return m, nil
	case "down", "j":
		m.menu = m.menu.CursorDown()
		return m, nil
	case "enter":
		return m.runSelectedMenuCommand()
	}
	return m, nil
}

func (m *Model) handleTerminalKey(k tea.KeyPressMsg) (tui.DeviceView, tea.Cmd) {
	switch k.String() {
	case "esc":
		return m, func() tea.Msg { return tui.CloseDeviceMsg{} }
	case "ctrl+l":
		m.scrollback.Clear()
		if m.session.Active() && !m.disconnected && !m.runner.busy() {
			return m, m.session.Send([]byte("\r"))
		}
		return m, nil
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

func (m *Model) handleDetailKey(k tea.KeyPressMsg) (tui.DeviceView, tea.Cmd) {
	switch k.String() {
	case "esc":
		// Back to menu; clear the detail pane so a stale result doesn't
		// surface if the user opens a different command later.
		m.mode = modeMenu
		m.detailContent = ""
		m.detailErr = nil
		m.detailCmdID = ""
		return m, nil
	case "up", "k":
		m.scroll = m.scroll.SetYOffset(m.scroll.YOffset() - 1)
		return m, nil
	case "down", "j":
		m.scroll = m.scroll.SetYOffset(m.scroll.YOffset() + 1)
		return m, nil
	}
	return m, nil
}

func (m *Model) toggleTerminal() (tui.DeviceView, tea.Cmd) {
	if m.mode == modeTerminal {
		m.mode = modeMenu
	} else {
		m.mode = modeTerminal
	}
	return m, nil
}

func (m *Model) runSelectedMenuCommand() (tui.DeviceView, tea.Cmd) {
	sel := m.menu.Selected()
	if sel == nil {
		return m, nil
	}
	cmd := findCommand(sel.ID)
	if cmd == nil {
		return m, nil
	}
	if !m.session.Active() || m.disconnected {
		return m, nil
	}
	if m.runner.busy() {
		// Don't stack commands; just ignore. The detail pane already shows
		// the running indicator for the in-flight one.
		return m, nil
	}
	m.detailCmdID = cmd.ID
	m.detailContent = ""
	m.detailErr = nil
	m.detailRunning = true
	m.mode = modeDetail
	timeoutCmd := m.runner.start(cmd.ID)
	sendCmd := m.session.Send([]byte(cmd.Cmd + "\r\n"))
	return m, tea.Batch(timeoutCmd, sendCmd)
}

// startHeaderCapture seeds the header-capture queue and runs the first one.
// Subsequent commands are issued by handleCommandResult / advanceHeaderCapture
// once each prior one's prompt arrives.
//
// device_info is a legacy alias that outputs underscore-joined keys
// (firmware_version, hardware_ver, ...). Power info is only available as the
// modern "info power" subcommand, which uses dot-joined keys (charge.level,
// battery.voltage, ...).
func (m *Model) startHeaderCapture() tea.Cmd {
	if !m.session.Active() {
		return nil
	}
	m.headerCaptureQueue = []string{"device_info", "info power"}
	return m.advanceHeaderCapture()
}

// advanceHeaderCapture pops the next command from the header queue and issues
// it. Returns nil when the queue is drained.
func (m *Model) advanceHeaderCapture() tea.Cmd {
	if len(m.headerCaptureQueue) == 0 {
		return nil
	}
	if m.runner.busy() {
		// A user-triggered command is currently running. Defer header capture
		// until it completes; advanceHeaderCapture will be re-driven from
		// handleCommandResult once the runner is idle. For simplicity in v1
		// the queue just stays put and the user can ctrl+r when they want it.
		return nil
	}
	if !m.session.Active() || m.disconnected {
		m.headerCaptureQueue = nil
		return nil
	}
	next := m.headerCaptureQueue[0]
	m.headerCaptureQueue = m.headerCaptureQueue[1:]
	m.headerCaptureActive = true
	timeoutCmd := m.runner.start("header:" + next)
	sendCmd := m.session.Send([]byte(next + "\r\n"))
	return tea.Batch(timeoutCmd, sendCmd)
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

	// In terminal/detail modes, pin scroll to the bottom for terminal so the
	// latest output is always visible. Detail mode stays at the user's
	// scroll position.
	if m.mode == modeTerminal {
		lines := strings.Count(body, "\n")
		if lines > bodyHeight {
			m.scroll = m.scroll.ScrollToLine(lines)
		}
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
	switch m.mode {
	case modeMenu:
		return m.renderMenuBody()
	case modeTerminal:
		return m.renderTerminalBody()
	case modeDetail:
		return m.renderDetailBody()
	}
	return ""
}

func (m *Model) renderMenuBody() string {
	m.menu = m.menu.SetWidth(m.width - 1)
	return m.menu.View()
}

func (m *Model) renderTerminalBody() string {
	t := m.theme.Terminal
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

func (m *Model) renderDetailBody() string {
	t := m.theme.Terminal
	cmd := findCommand(m.detailCmdID)
	header := ""
	if cmd != nil {
		header = t.HeaderVal.Render("  "+cmd.Label) + "  " + t.Dim.Render("("+cmd.Cmd+")")
	}
	switch {
	case m.detailRunning:
		return header + "\n\n" + t.Dim.Render("  Running…")
	case m.detailErr != nil:
		return header + "\n\n" + lipgloss.NewStyle().Foreground(t.Disconnect).Render("  "+m.detailErr.Error())
	case m.detailContent != "":
		return header + "\n\n" + m.detailContent
	default:
		return header + "\n\n" + t.Dim.Render("  (no result)")
	}
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
	var bindings []components.KeyBinding
	switch m.mode {
	case modeMenu:
		bindings = []components.KeyBinding{
			{Keys: "↑↓", Description: "navigate"},
			{Keys: "enter", Description: "run"},
			{Keys: "ctrl+t", Description: "terminal"},
			{Keys: "ctrl+r", Description: "refresh"},
			{Keys: "esc", Description: "close"},
		}
	case modeTerminal:
		bindings = []components.KeyBinding{
			{Keys: "esc", Description: "close"},
			{Keys: "ctrl+l", Description: "clear"},
			{Keys: "ctrl+t", Description: "menu"},
			{Keys: "ctrl+r", Description: "refresh"},
			{Keys: "keys", Description: "→ device"},
		}
	case modeDetail:
		bindings = []components.KeyBinding{
			{Keys: "↑↓", Description: "scroll"},
			{Keys: "esc", Description: "back"},
			{Keys: "ctrl+t", Description: "terminal"},
		}
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
