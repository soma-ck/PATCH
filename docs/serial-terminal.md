# `serialterm/` Package Reference

The `serialterm/` package is the shared I/O layer every device view composes for talking to a serial port. It does three things:

1. **Drive the port lifecycle** (`Session`) — open, read, write, close; all through Bubbletea messages.
2. **Sanitize incoming bytes** (`Scrollback`) — turn raw serial output into terminal lines without letting rogue escape sequences corrupt the rendered frame.
3. **Translate keystrokes** (`KeyToBytes`) — map Bubbletea key presses to the bytes a serial device expects.

If you are adding a new device view, you reuse all three verbatim. You should not need to edit anything under `serialterm/` unless you are adding a new transport (HID, TCP) or extending the ANSI parser.

## 1. `Session` (`session.go`)

`Session` owns the I/O lifecycle for one open transport.

```go
type Session struct {
    transport Transport       // io.ReadWriteCloser — the serial port
    rxCh      chan tea.Msg    // read loop posts RxMsg / RxClosedMsg here
    doneCh    chan struct{}   // closed to signal read loop to exit
}
```

All three fields are zero-valued in a freshly constructed `Session{}`. That is intentional — device views always embed a pointer to `Session{}` and replace its fields on `OpenedMsg`. Never construct a `Session` with its own fields; always let `OpenSerialSession` produce the `OpenedMsg` and call `Install`.

### Lifecycle

```
┌──────────────────────────┐
│ Open(info) returns       │
│   OpenSerialSession(...)│  — returns tea.Cmd
└──────────┬───────────────┘
           │ Bubbletea runtime executes the cmd
           ▼
┌──────────────────────────┐
│ goroutine: opens port    │
│   success → OpenedMsg    │
│   failure → OpenErrMsg   │
└──────────┬───────────────┘
           │ message delivered to Update
           ▼
┌──────────────────────────┐
│ Update: case OpenedMsg   │
│   session.Install(msg)   │
│   return session.WaitRx()│
└──────────┬───────────────┘
           │ WaitRx blocks on rxCh until a message arrives
           ▼
┌──────────────────────────┐
│ readLoop goroutine       │
│   reads bytes → RxMsg    │
│   transport error →      │
│     RxClosedMsg          │
└──────────┬───────────────┘
           │ RxMsg (or RxClosedMsg)
           ▼
┌──────────────────────────┐
│ Update: case RxMsg       │
│   scrollback.Write(data) │
│   return session.WaitRx()│ ◄── re-arm, critical
└──────────────────────────┘
```

### Method reference

| Method | Location | Purpose |
|--------|----------|---------|
| `OpenSerialSession(path, baud) tea.Cmd` | `session.go:27-38` | Top-level constructor. Returns a command that opens the port off the event loop and posts `OpenedMsg` or `OpenErrMsg`. |
| `Install(msg OpenedMsg)` | `session.go:42-46` | Stores the transport and channels from `OpenedMsg` onto the session. Call this on `case OpenedMsg` in `Update`. |
| `Send(data []byte) tea.Cmd` | `session.go:50-59` | Returns a command that writes bytes to the transport off the event loop. Nil-safe: returns `nil` if the session is closed or the data is empty. Posts `TxDoneMsg`. |
| `WaitRx() tea.Cmd` | `session.go:64-76` | Returns a command that blocks on `rxCh` and posts the next `RxMsg` or `RxClosedMsg`. **Must be re-armed after every `RxMsg`**, or the listen loop stalls. |
| `Active() bool` | `session.go:79-81` | True iff `transport` is non-nil. Guard key-send logic with this. |
| `Close()` | `session.go:85-98` | Closes `doneCh` to signal the read loop, closes the transport, clears all fields. Safe on a zero-valued session. |
| `MarkDisconnected()` | `session.go:103-113` | Called on `RxClosedMsg`. Closes the transport but **does not** close `doneCh` — the read loop has already returned. |

### The single most important rule

> **After every `RxMsg`, return `session.WaitRx()`.**

`WaitRx` reads exactly one message from `rxCh` and returns. If you forget to call it again after processing the RX bytes, the read loop fills `rxCh` (capacity 64) and then blocks on its next `case out <- RxMsg{...}:`. No more bytes reach the view until the channel drains — which it never will, because nobody is reading.

Both existing views follow this pattern (`devices/generic/generic.go:82-83`, `devices/flipper/flipper.go:138`).

### `Close()` vs. `MarkDisconnected()`

These are not interchangeable. Calling the wrong one will either leak a goroutine or panic.

- **`Close()`** — call from `DeviceView.Close()` when the *user* is tearing down the modal and the read loop might still be running. It closes `doneCh`, which the read loop is selecting on.
- **`MarkDisconnected()`** — call from `case RxClosedMsg` in `Update`. The read loop has already returned (it's the one that posted `RxClosedMsg`). Closing `doneCh` here would only matter if someone called `Close` later — but because `MarkDisconnected` sets `doneCh = nil`, that later `Close` skips the close and is safe. Never close `doneCh` twice.

## 2. Message types (`messages.go`)

Five `tea.Msg` types flow through the system. All have exported fields so device views can pattern-match on them.

| Type | Posted when | Fields |
|------|-------------|--------|
| `OpenedMsg` | Port open succeeded | `Transport`, `RxCh`, `DoneCh` |
| `OpenErrMsg` | Port open failed | `Err` |
| `RxMsg` | Read loop pulled `n > 0` bytes | `Data []byte` (new allocation per chunk) |
| `RxClosedMsg` | Read loop exited (clean or error) | `Err` (nil on clean close) |
| `TxDoneMsg` | `Session.Send` finished writing | `Err` (nil on success) |

`RxMsg.Data` is a **fresh allocation** each call (`session.go:139-140` — `chunk := make([]byte, n); copy(chunk, buf[:n])`). You can hold on to it safely without worrying about the underlying read buffer being reused.

## 3. `Transport` (`transport.go`)

```go
type Transport interface {
    io.ReadWriteCloser
}
```

That's it. The interface is deliberately minimal — `Session` only ever calls `Read`, `Write`, and `Close`. The concrete implementation shipping today is whatever `go.bug.st/serial` returns from `serial.Open`.

`OpenSerial(path, baud)` (`transport.go:19-27`) hard-codes 8N1 framing (8 data bits, no parity, 1 stop bit). Every USB-CDC device we currently target uses this; if a future device needs different framing, add a second constructor alongside `OpenSerial` rather than parameterising the existing one.

### Adding a new transport

The `Transport` interface is the extension point. A hypothetical `OpenHID(vid, pid)` would return `Transport` just as `OpenSerial` does, and `Session` would compose it without change. The `DeviceView` implementations would be unaffected — they only know about `Session`.

## 4. `Scrollback` (`scrollback.go`)

`Scrollback` is a **line-mode terminal**, not a full VT100 emulator. It models the in-progress line as a slice of cells indexed by visible column, with a cursor that moves over them, so devices that draw via cursor commands (Flipper Zero, Linux shells) render correctly without the parser becoming a full screen emulator.

### What it preserves

- **SGR sequences** (`\e[...m`) — bold, colors, underline. These are stored as transitions tied to specific cells, so colors render correctly even after mid-line overwrites (e.g., the cursor moves left and writes new bytes over old ones).
- **SGR carry-forward** — if the device emits `\e[31mHello\nWorld`, the `Hello` line ends mid-red-color and the `World` line starts with the same SGR prepended on its first cell. This matches real-terminal behaviour where colors persist across newlines until an explicit reset.

### Line-edit CSI sequences it honors

These let CLI line editors (history scroll, mid-line backspace, cursor-arrow navigation) draw correctly:

| Sequence | Name | Effect |
|----------|------|--------|
| `\e[<n>D` | CUB | Move cursor left by `n` (default 1) |
| `\e[<n>C` | CUF | Move cursor right by `n` (default 1); writes past EOL pad with spaces |
| `\e[<n>G` | CHA | Move cursor to column `n` (1-indexed); used by line-redraw on history navigation |
| `\e[<n>K` | EL  | Erase in line: 0=cursor to end, 1=start to cursor (replace with spaces), 2=whole line |

### What it strips

- **All other CSI sequences** (CUP, CUU/CUD, scroll, save/restore, alternate screens, etc.) are consumed but discarded — they don't apply in a line-mode model and would corrupt the layout if forwarded.
- **`ESC P`, `ESC ]`, `ESC X`, `ESC ^`, `ESC _`** — DCS / OSC / SOS / PM / APC introducers. These would normally require tracking until `ST`, but since we're not emulating, dropping the intro is safe enough for the devices we target.
- **BEL** (`0x07`) — dropped silently.

### What it handles as C0 controls

| Byte | Action |
|------|--------|
| `\n` (LF) | Commit current line to history, start a fresh line with carried SGR |
| `\r` (CR) | Flag the next byte as a line reset |
| `\r\n` (CRLF) | Treated as a single LF (the pending-CR flag is swallowed) |
| Standalone `\r` | Reset the current line to the carried SGR on the next byte |
| `\b` (BS) | Cursor left; additionally drops the trailing cell if the cursor was at end-of-line (legacy destructive-backspace pattern) |
| `\t` (HT) | Write four spaces |
| `\x20`–`\x7e` | Write to the cell at the cursor (overwriting if mid-line), increment cursor |

### Ring-buffer capacity

`NewScrollback(capacity int)` creates a scrollback; capacity `<= 0` uses `defaultScrollbackCapacity = 4000` (`scrollback.go:7`). When committed lines exceed capacity, the oldest are dropped (`scrollback.go:186-189`).

### API

```go
sb := serialterm.NewScrollback(0)  // default capacity
sb.Write(bytes)                    // feed bytes through the state machine
lines := sb.Lines()                // committed lines + in-progress line
sb.Clear()                         // reset all state (e.g., on ctrl+l)
```

### Spec

`serialterm/ansi_test.go` is the behavioural spec. If you think you need to modify the parser, write the failing case there first.

## 5. `KeyToBytes` (`keys.go`)

`KeyToBytes(k tea.KeyPressMsg) []byte` (`keys.go:8-81`) translates a Bubbletea key press into the byte sequence a serial device expects.

| Key | Bytes |
|-----|-------|
| `enter` | `\r` |
| `tab` | `\t` |
| `backspace` | `0x7f` (DEL — the Unix convention) |
| `up` / `down` / `right` / `left` | `\x1b[A` / `B` / `C` / `D` |
| `home` / `end` | `\x1b[H` / `\x1b[F` |
| `pgup` / `pgdown` | `\x1b[5~` / `\x1b[6~` |
| `delete` | `\x1b[3~` |
| `ctrl+a` … `ctrl+z` | ASCII `0x01` … `0x1a` (Ctrl codes) |
| `ctrl+@` / `ctrl+space` | `0x00` (NUL) |
| `space` | `' '` (`0x20`) |
| any key with `.Text != ""` | the literal `.Text` bytes |
| everything else | `nil` — swallowed |

**Note the gaps.** `ctrl+l` and `ctrl+m` are not in the map — the view handles `ctrl+l` locally (clear terminal) and `ctrl+m` would be a duplicate of `enter`. `ctrl+c` (`0x03`) is present and *will* be sent to the device; this is why `ctrl+c` does not quit from inside a device modal.

### Extending

If a new device needs a key that isn't in the table, add a `case` in `keys.go`. Don't override it in the device view — keep the mapping in one place so all devices behave consistently.

## 6. Read-loop internals (`session.go:119-147`)

The read loop runs in its own goroutine. It is short enough to reproduce here:

```go
func readLoop(t Transport, out chan<- tea.Msg, done <-chan struct{}) {
    defer close(out)
    buf := make([]byte, 4096)
    for {
        select {
        case <-done:
            return
        default:
        }
        n, err := t.Read(buf)
        if err != nil {
            select {
            case <-done:
            case out <- RxClosedMsg{Err: err}:
            }
            return
        }
        if n <= 0 {
            continue
        }
        chunk := make([]byte, n)
        copy(chunk, buf[:n])
        select {
        case <-done:
            return
        case out <- RxMsg{Data: chunk}:
        }
    }
}
```

Three things worth noting:

1. **`defer close(out)`**. When the loop exits, `out` is closed. Any currently-blocked `WaitRx` (`session.go:69-75`) sees `ok = false` and returns `RxClosedMsg{}`. No goroutine leaks.
2. **Non-blocking `done` check before `Read`**. Without this, a `Close()` during an in-progress `Read` would have to wait for the kernel to return from the blocking read before the goroutine could exit. The non-blocking check isn't a full solution (a `Close` mid-`Read` still waits), but it does catch the common case where the app cancels before `Read` is called.
3. **`RxClosedMsg` send is racey with `done`**. If the transport errors at the same moment the user closes the modal, the select at `session.go:130-132` chooses whichever arrives first — either `done` wins and the goroutine exits silently, or the message is delivered but then consumed by whoever is draining `rxCh` after close. Either is fine.

**Channel capacity: 64** (`session.go:33`). Sized generously — if the device floods the port faster than the event loop can process `RxMsg`, the read goroutine blocks on the `select out <- RxMsg`, which *doesn't* drop data. It just backpressures the device via the OS read buffer. In practice at 115200 baud this never matters.

## 7. Do's and don'ts

**Do**

- Always re-arm `session.WaitRx()` after every `RxMsg` in your `Update`.
- Call `session.MarkDisconnected()` on `RxClosedMsg`, not `Close()`.
- Call `session.Close()` from your `DeviceView.Close()` — it's safe on a zero-valued session.
- Guard key sends with `if !m.session.Active() || m.disconnected { return m, nil }`.
- Hold on to `RxMsg.Data` freely — the read loop allocated it just for you.

**Don't**

- Don't spawn your own goroutines from `Update`. Use `tea.Cmd` / `tea.Batch` / `tea.Tick`. The one exception — the read loop goroutine itself — is already owned by `Session`.
- Don't modify `serialterm/` for per-device quirks. If Flipper needs a capture window and Generic doesn't, that lives in the Flipper package, not in `Session`.
- Don't create the SGR / ANSI parser's state by hand. Only feed bytes through `Write`; read them back through `Lines`.
- Don't assume `Session.Send` is synchronous. It returns a `tea.Cmd` — the write happens off the event loop and completes later via `TxDoneMsg`.
- Don't close `doneCh` twice. If you're confused about whether to call `Close` or `MarkDisconnected`, re-read §1.
