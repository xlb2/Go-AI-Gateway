//go:build linux

package cli_test

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"go_im_gateway/internal/cli"
)

func TestCLIInteractiveInputEditing(t *testing.T) {
	master, slave := openPTY(t)
	defer master.Close()
	monitorFD, err := syscall.Dup(int(slave.Fd()))
	if err != nil {
		t.Fatalf("duplicate PTY slave: %v", err)
	}
	monitor := os.NewFile(uintptr(monitorFD), "pty-monitor")
	defer monitor.Close()
	setPTYWindowSize(t, monitor, 80, 24)
	originalState := readPTYState(t, monitor)

	output := &ptyOutput{changed: make(chan struct{}, 1)}
	go output.read(master)

	received := make(chan string, 3)
	r := runner{
		run: func(_ context.Context, _ uint, input string, _ func(string)) (string, error) {
			received <- input
			return "ok", nil
		},
		approve: func(_ context.Context, _ uint, input string) (bool, string) {
			if input == "auth:reject" {
				t.Error("bracketed paste was dispatched as an approval command")
			}
			return false, ""
		},
	}
	done := make(chan error, 1)
	go func() {
		done <- cli.Run(context.Background(), r, 1, slave, slave, nil)
	}()

	output.waitCount(t, "\x1b[?2004h", 1)
	output.waitRuleCount(t, 80, 2)
	setPTYWindowSize(t, monitor, 60, 24)
	if err := syscall.Kill(os.Getpid(), syscall.SIGWINCH); err != nil {
		t.Fatalf("signal terminal resize: %v", err)
	}
	output.waitRuleCount(t, 60, 2)
	if _, err := io.WriteString(master, "abcd\x1b[D\x1b[DXY\x1b[D\x1b[C\x1b[3~\x1b[F\x7f\x1b[Hz\r"); err != nil {
		t.Fatal(err)
	}
	if got := receiveInput(t, received); got != "zabXY" {
		t.Fatalf("edited input = %q, want %q", got, "zabXY")
	}

	output.waitCount(t, "\x1b[?2004h", 2)
	if _, err := io.WriteString(master, "\x1b[A\r"); err != nil {
		t.Fatal(err)
	}
	if got := receiveInput(t, received); got != "zabXY" {
		t.Fatalf("history input = %q, want %q", got, "zabXY")
	}

	output.waitCount(t, "\x1b[?2004h", 3)
	if _, err := io.WriteString(master, "\x1b[200~/exit\r\nPASTE-B\r\nthird line\x1b[201~"); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(master, "\x1b[AX"); err != nil {
		t.Fatal(err)
	}
	output.waitCount(t, "third line", 1)
	output.waitCount(t, "PASTE-BX", 1)
	assertNoInput(t, received)
	if _, err := io.WriteString(master, "\r"); err != nil {
		t.Fatal(err)
	}
	if got := receiveInput(t, received); got != "/exit\nPASTE-BX\nthird line" {
		t.Fatalf("bracketed paste = %q, want %q", got, "/exit\nPASTE-BX\nthird line")
	}

	output.waitCount(t, "\x1b[?2004h", 4)
	assertNoInput(t, received)
	if _, err := io.WriteString(master, "\x1b[200~/exit\x1b[201~"); err != nil {
		t.Fatal(err)
	}
	assertNoInput(t, received)
	if _, err := io.WriteString(master, "\r"); err != nil {
		t.Fatal(err)
	}
	if got := receiveInput(t, received); got != "/exit" {
		t.Fatalf("command-shaped paste = %q, want message body %q", got, "/exit")
	}

	output.waitCount(t, "\x1b[?2004h", 5)
	assertNoInput(t, received)
	if _, err := io.WriteString(master, "\x1b[200~auth:reject\x1b[201~"); err != nil {
		t.Fatal(err)
	}
	assertNoInput(t, received)
	if _, err := io.WriteString(master, "\r"); err != nil {
		t.Fatal(err)
	}
	if got := receiveInput(t, received); got != "auth:reject" {
		t.Fatalf("approval-shaped paste = %q, want message body %q", got, "auth:reject")
	}

	output.waitCount(t, "\x1b[?2004h", 6)
	assertNoInput(t, received)
	if _, err := io.WriteString(master, "/paste\r"); err != nil {
		t.Fatal(err)
	}
	output.waitCount(t, "Paste mode:", 1)
	output.waitCount(t, "... ", 1)
	if _, err := io.WriteString(master, "first\r\rsecond\r/send\r"); err != nil {
		t.Fatal(err)
	}
	if got := receiveInput(t, received); got != "first\n\nsecond" {
		t.Fatalf("pasted input = %q, want %q", got, "first\n\nsecond")
	}

	output.waitCount(t, "\x1b[?2004h", 7)
	assertNoInput(t, received)
	if _, err := io.WriteString(master, "\x03"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("CLI did not exit after Ctrl+C")
	}
	output.waitCount(t, "^C", 1)
	output.waitCount(t, "\x1b[?2004l", 11)
	output.mu.Lock()
	terminalOutput := output.data.String()
	output.mu.Unlock()
	if strings.Contains(terminalOutput, "You") {
		t.Fatalf("interactive input still shows the You label: %q", terminalOutput)
	}
	if enabled, disabled := strings.Count(terminalOutput, "\x1b[?2004h"), strings.Count(terminalOutput, "\x1b[?2004l"); enabled != disabled {
		t.Fatalf("bracketed paste mode was not balanced: enabled=%d disabled=%d", enabled, disabled)
	}
	if restoredState := readPTYState(t, monitor); restoredState != originalState {
		t.Fatalf("terminal state was not restored: before=%+v after=%+v", originalState, restoredState)
	}
}

func TestCLIUsesLineReaderWhenInputIsNotTerminal(t *testing.T) {
	master, terminal := openPTY(t)
	defer master.Close()
	output := &ptyOutput{changed: make(chan struct{}, 1)}
	go output.read(master)

	input, inputWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer inputWriter.Close()

	received := make(chan string, 1)
	r := runner{run: func(_ context.Context, _ uint, text string, _ func(string)) (string, error) {
		received <- text
		return "ok", nil
	}}
	done := make(chan error, 1)
	go func() {
		done <- cli.Run(context.Background(), r, 1, input, terminal, nil)
	}()
	if _, err := io.WriteString(inputWriter, "piped input\n"); err != nil {
		t.Fatal(err)
	}
	if err := inputWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if got := receiveInput(t, received); got != "piped input" {
		t.Fatalf("piped input = %q, want %q", got, "piped input")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("CLI returned error for piped input: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("CLI did not return after piped input reached EOF")
	}
}

func receiveInput(t *testing.T, received <-chan string) string {
	t.Helper()
	select {
	case input := <-received:
		return input
	case <-time.After(5 * time.Second):
		t.Fatal("CLI did not submit edited input")
		return ""
	}
}

func assertNoInput(t *testing.T, received <-chan string) {
	t.Helper()
	select {
	case input := <-received:
		t.Fatalf("input submitted before Enter: %q", input)
	case <-time.After(100 * time.Millisecond):
	}
}

func openPTY(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	const (
		tiocgptn    = uintptr(0x80045430)
		tiocspTtlck = uintptr(0x40045431)
	)
	masterFD, err := syscall.Open("/dev/ptmx", syscall.O_RDWR|syscall.O_NOCTTY|syscall.O_CLOEXEC, 0)
	if err != nil {
		t.Fatalf("open PTY master: %v", err)
	}
	master := os.NewFile(uintptr(masterFD), "pty-master")
	t.Cleanup(func() { _ = master.Close() })

	var unlock int32
	if err := ptyIoctl(masterFD, tiocspTtlck, unsafe.Pointer(&unlock)); err != nil {
		t.Fatalf("unlock PTY: %v", err)
	}
	var number uint32
	if err := ptyIoctl(masterFD, tiocgptn, unsafe.Pointer(&number)); err != nil {
		t.Fatalf("get PTY number: %v", err)
	}
	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", number), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatalf("open PTY slave: %v", err)
	}
	t.Cleanup(func() { _ = slave.Close() })
	return master, slave
}

func ptyIoctl(fd int, request uintptr, value unsafe.Pointer) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), request, uintptr(value))
	if errno != 0 {
		return errno
	}
	return nil
}

func setPTYWindowSize(t *testing.T, file *os.File, columns, rows uint16) {
	t.Helper()
	const tiocswinsz = uintptr(0x5414)
	winsize := struct {
		Row    uint16
		Col    uint16
		Xpixel uint16
		Ypixel uint16
	}{Row: rows, Col: columns}
	if err := ptyIoctl(int(file.Fd()), tiocswinsz, unsafe.Pointer(&winsize)); err != nil {
		t.Fatalf("set PTY window size: %v", err)
	}
}

func readPTYState(t *testing.T, file *os.File) syscall.Termios {
	t.Helper()
	var state syscall.Termios
	if err := ptyIoctl(int(file.Fd()), uintptr(syscall.TCGETS), unsafe.Pointer(&state)); err != nil {
		t.Fatalf("read PTY state: %v", err)
	}
	return state
}

type ptyOutput struct {
	mu      sync.Mutex
	data    strings.Builder
	changed chan struct{}
}

func (o *ptyOutput) read(master *os.File) {
	buffer := make([]byte, 4096)
	for {
		n, err := master.Read(buffer)
		if n > 0 {
			o.mu.Lock()
			o.data.Write(buffer[:n])
			o.mu.Unlock()
			select {
			case o.changed <- struct{}{}:
			default:
			}
		}
		if err != nil {
			return
		}
	}
}

func (o *ptyOutput) waitCount(t *testing.T, text string, count int) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		o.mu.Lock()
		current := o.data.String()
		o.mu.Unlock()
		if strings.Count(current, text) >= count {
			return
		}
		select {
		case <-o.changed:
		case <-deadline.C:
			t.Fatalf("output did not contain %q %d times: %q", text, count, current)
		}
	}
}

func (o *ptyOutput) waitRuleCount(t *testing.T, columns, count int) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		o.mu.Lock()
		current := stripPTYControlCodes(o.data.String())
		o.mu.Unlock()
		got := countRuleRuns(current, columns-1)
		if got >= count {
			return
		}
		select {
		case <-o.changed:
		case <-deadline.C:
			t.Fatalf("output did not contain %d exact %d-column border runs: %q", count, columns, current)
		}
	}
}

func countRuleRuns(text string, cells int) int {
	runes := []rune(text)
	count := 0
	for i := 0; i < len(runes); {
		if runes[i] != '─' {
			i++
			continue
		}
		start := i
		for i < len(runes) && runes[i] == '─' {
			i++
		}
		if i-start == cells {
			count++
		}
	}
	return count
}

func stripPTYControlCodes(text string) string {
	var clean strings.Builder
	for i := 0; i < len(text); {
		if text[i] != '\x1b' {
			clean.WriteByte(text[i])
			i++
			continue
		}
		if i+1 >= len(text) || text[i+1] != '[' {
			i++
			continue
		}
		i += 2
		for i < len(text) && (text[i] < 0x40 || text[i] > 0x7e) {
			i++
		}
		if i < len(text) {
			i++
		}
	}
	return clean.String()
}
