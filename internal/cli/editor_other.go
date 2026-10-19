//go:build !linux

package cli

import (
	"bufio"
	"os"
)

func interactiveEditorSupported() bool { return false }

func terminalResizeSignal() os.Signal { return nil }

func escapeSequenceAvailable(*bufio.Reader, *os.File) bool { return false }
