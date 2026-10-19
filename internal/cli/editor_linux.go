//go:build linux

package cli

import (
	"bufio"
	"os"
	"syscall"
)

func interactiveEditorSupported() bool { return true }

func terminalResizeSignal() os.Signal { return syscall.SIGWINCH }

func escapeSequenceAvailable(reader *bufio.Reader, input *os.File) bool {
	if reader.Buffered() > 0 {
		return true
	}
	fd := int(input.Fd())
	var readSet syscall.FdSet
	if fd < 0 || fd >= len(readSet.Bits)*64 {
		return false
	}
	readSet.Bits[fd/64] |= int64(1) << uint(fd%64)
	timeout := syscall.Timeval{Usec: 40000}
	ready, err := syscall.Select(fd+1, &readSet, nil, nil, &timeout)
	return err == nil && ready > 0
}
