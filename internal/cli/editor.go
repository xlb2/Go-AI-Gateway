package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/term"
)

const maxInteractiveInputBytes = 64 * 1024

type editorKey uint8

const (
	keyIgnore editorKey = iota
	keyRune
	keyEnter
	keyInterrupt
	keyBackspace
	keyDelete
	keyLeft
	keyRight
	keyHome
	keyEnd
	keyUp
	keyDown
	keyPaste
)

type keyEvent struct {
	kind editorKey
	rune rune
	text string
}

type keyResult struct {
	event keyEvent
	err   error
}

type editedInput struct {
	text   string
	pasted bool
}

type lineEditor struct {
	input             *os.File
	terminal          *os.File
	output            io.Writer
	reader            *bufio.Reader
	history           []string
	lastWidth         int
	lastRowWidths     []int
	lastCursorRow     int
	lastCursorCol     int
	lastRenderedRows  int
	hasRenderedBuffer bool
}

func newLineEditor(input io.ReadCloser, output io.Writer, terminal *os.File) *lineEditor {
	file, ok := input.(*os.File)
	if !ok || terminal == nil {
		return nil
	}
	if !interactiveEditorSupported() || !term.IsTerminal(int(file.Fd())) {
		return nil
	}
	return &lineEditor{input: file, terminal: terminal, output: output, reader: bufio.NewReader(file)}
}

func (e *lineEditor) readLine(ctx context.Context, prefix string, interrupts <-chan os.Signal, saveHistory bool) (line editedInput, interrupted bool, retErr error) {
	state, err := term.MakeRaw(int(e.input.Fd()))
	if err != nil {
		return editedInput{}, false, fmt.Errorf("enable terminal input: %w", err)
	}
	defer func() {
		if _, disableErr := io.WriteString(e.output, "\x1b[?2004l"); retErr == nil && disableErr != nil {
			retErr = fmt.Errorf("disable bracketed paste: %w", disableErr)
		}
		if restoreErr := term.Restore(int(e.input.Fd()), state); retErr == nil && restoreErr != nil {
			retErr = fmt.Errorf("restore terminal input: %w", restoreErr)
		}
	}()
	if _, err := io.WriteString(e.output, "\x1b[?2004h"); err != nil {
		return editedInput{}, false, fmt.Errorf("enable bracketed paste: %w", err)
	}
	resizeEvents := make(chan os.Signal, 1)
	if resizeSignal := terminalResizeSignal(); resizeSignal != nil {
		signal.Notify(resizeEvents, resizeSignal)
		defer signal.Stop(resizeEvents)
	}

	readCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	keys := make(chan keyResult, 1)
	go func() {
		defer close(keys)
		for {
			event, err := e.readKey()
			select {
			case keys <- keyResult{event: event, err: err}:
			case <-readCtx.Done():
				return
			}
			if err != nil || event.kind == keyEnter || event.kind == keyInterrupt {
				return
			}
		}
	}()

	buffer := make([]rune, 0, 64)
	cursor := 0
	inputBytes := 0
	hadPaste := false
	historyIndex := len(e.history)
	draft := ""
	if err := e.redraw(prefix, buffer, cursor); err != nil {
		return editedInput{}, false, err
	}
	for {
		select {
		case <-ctx.Done():
			return editedInput{}, false, ctx.Err()
		case <-interrupts:
			if err := e.endRenderedInput(); err != nil {
				return editedInput{}, false, err
			}
			return editedInput{}, true, nil
		case <-resizeEvents:
			if err := e.redraw(prefix, buffer, cursor); err != nil {
				return editedInput{}, false, err
			}
		case result, ok := <-keys:
			if !ok {
				return editedInput{}, false, io.EOF
			}
			if result.err != nil {
				return editedInput{}, false, result.err
			}
			switch result.event.kind {
			case keyEnter:
				if err := e.endRenderedInput(); err != nil {
					return editedInput{}, false, err
				}
				line = editedInput{text: string(buffer), pasted: hadPaste}
				if saveHistory && strings.TrimSpace(line.text) != "" {
					e.addHistory(line.text)
				}
				return line, false, nil
			case keyInterrupt:
				if err := e.endRenderedInput(); err != nil {
					return editedInput{}, false, err
				}
				if _, err := io.WriteString(e.output, "^C\r\n"); err != nil {
					return editedInput{}, false, err
				}
				return editedInput{}, true, nil
			case keyRune:
				r := result.event.rune
				if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
					break
				}
				width := utf8.RuneLen(r)
				if width < 0 || inputBytes+width > maxInteractiveInputBytes {
					return editedInput{}, false, fmt.Errorf("input exceeds 64 KiB; nothing sent")
				}
				buffer = insertRunes(buffer, cursor, []rune{r})
				cursor++
				inputBytes += width
				historyIndex = len(e.history)
				draft = string(buffer)
			case keyPaste:
				pasted := sanitizePaste(result.event.text)
				if inputBytes+len(pasted) > maxInteractiveInputBytes {
					return editedInput{}, false, fmt.Errorf("input exceeds 64 KiB; nothing sent")
				}
				pasteRunes := []rune(pasted)
				buffer = insertRunes(buffer, cursor, pasteRunes)
				cursor += len(pasteRunes)
				inputBytes += len(pasted)
				hadPaste = true
				historyIndex = len(e.history)
				draft = string(buffer)
			case keyBackspace:
				if cursor > 0 {
					inputBytes -= utf8.RuneLen(buffer[cursor-1])
					copy(buffer[cursor-1:], buffer[cursor:])
					buffer = buffer[:len(buffer)-1]
					cursor--
					historyIndex = len(e.history)
					draft = string(buffer)
				}
			case keyDelete:
				if cursor < len(buffer) {
					inputBytes -= utf8.RuneLen(buffer[cursor])
					copy(buffer[cursor:], buffer[cursor+1:])
					buffer = buffer[:len(buffer)-1]
					historyIndex = len(e.history)
					draft = string(buffer)
				}
			case keyLeft:
				if cursor > 0 {
					cursor--
				}
			case keyRight:
				if cursor < len(buffer) {
					cursor++
				}
			case keyHome:
				cursor = lineStart(buffer, cursor)
			case keyEnd:
				cursor = lineEnd(buffer, cursor)
			case keyUp:
				if containsNewline(buffer) && historyIndex == len(e.history) {
					cursor = moveVertical(buffer, cursor, -1)
				} else if historyIndex == len(e.history) {
					draft = string(buffer)
					if historyIndex > 0 {
						historyIndex--
						buffer = []rune(e.history[historyIndex])
						cursor = len(buffer)
						inputBytes = len(e.history[historyIndex])
					}
				} else if historyIndex > 0 {
					historyIndex--
					buffer = []rune(e.history[historyIndex])
					cursor = len(buffer)
					inputBytes = len(e.history[historyIndex])
				}
			case keyDown:
				if containsNewline(buffer) && historyIndex == len(e.history) {
					cursor = moveVertical(buffer, cursor, 1)
				} else if historyIndex < len(e.history) {
					historyIndex++
					if historyIndex == len(e.history) {
						buffer = []rune(draft)
						inputBytes = len(draft)
					} else {
						buffer = []rune(e.history[historyIndex])
						inputBytes = len(e.history[historyIndex])
					}
					cursor = len(buffer)
				}
			}
			if err := e.redraw(prefix, buffer, cursor); err != nil {
				return editedInput{}, false, err
			}
		}
	}
}

func (e *lineEditor) readKey() (keyEvent, error) {
	r, _, err := e.reader.ReadRune()
	if err != nil {
		return keyEvent{}, err
	}
	switch r {
	case '\x03':
		return keyEvent{kind: keyInterrupt}, nil
	case '\r', '\n':
		return keyEvent{kind: keyEnter}, nil
	case '\x7f', '\b':
		return keyEvent{kind: keyBackspace}, nil
	case '\x1b':
		return e.readEscape()
	}
	return keyEvent{kind: keyRune, rune: r}, nil
}

func (e *lineEditor) readEscape() (keyEvent, error) {
	if !escapeSequenceAvailable(e.reader, e.input) {
		return keyEvent{kind: keyIgnore}, nil
	}
	first, _, err := e.reader.ReadRune()
	if err != nil {
		return keyEvent{kind: keyIgnore}, nil
	}
	if first == 'O' {
		final, _, err := e.reader.ReadRune()
		if err != nil {
			return keyEvent{kind: keyIgnore}, nil
		}
		return keyEvent{kind: keyForFinal(final, "")}, nil
	}
	if first != '[' {
		_ = e.reader.UnreadRune()
		return keyEvent{kind: keyIgnore}, nil
	}
	var params strings.Builder
	for i := 0; i < 16; i++ {
		part, _, err := e.reader.ReadRune()
		if err != nil {
			return keyEvent{kind: keyIgnore}, nil
		}
		if part >= 0x40 && part <= 0x7e {
			if part == '~' && params.String() == "200" {
				text, err := e.readPaste()
				return keyEvent{kind: keyPaste, text: text}, err
			}
			return keyEvent{kind: keyForFinal(part, params.String())}, nil
		}
		params.WriteRune(part)
	}
	return keyEvent{kind: keyIgnore}, nil
}

func keyForFinal(final rune, params string) editorKey {
	switch final {
	case 'A':
		return keyUp
	case 'B':
		return keyDown
	case 'C':
		return keyRight
	case 'D':
		return keyLeft
	case 'H':
		return keyHome
	case 'F':
		return keyEnd
	case '~':
		switch strings.SplitN(params, ";", 2)[0] {
		case "1", "7":
			return keyHome
		case "3":
			return keyDelete
		case "4", "8":
			return keyEnd
		}
	}
	return keyIgnore
}

func (e *lineEditor) readPaste() (string, error) {
	const end = "\x1b[201~"
	var raw strings.Builder
	pending := make([]byte, 0, len(end))
	for {
		b, err := e.reader.ReadByte()
		if err != nil {
			return "", err
		}
		pending = append(pending, b)
		for len(pending) > 0 && !strings.HasPrefix(end, string(pending)) {
			_ = raw.WriteByte(pending[0])
			pending = pending[1:]
			if raw.Len()+len(pending) > maxInteractiveInputBytes {
				return "", fmt.Errorf("input exceeds 64 KiB; nothing sent")
			}
		}
		if string(pending) == end {
			break
		}
	}
	if raw.Len() > maxInteractiveInputBytes {
		return "", fmt.Errorf("input exceeds 64 KiB; nothing sent")
	}
	return raw.String(), nil
}

func sanitizePaste(text string) string {
	var cleaned strings.Builder
	lastWasCR := false
	for _, r := range text {
		if r == '\r' {
			cleaned.WriteByte('\n')
			lastWasCR = true
			continue
		}
		if r == '\n' && lastWasCR {
			lastWasCR = false
			continue
		}
		lastWasCR = false
		if r == '\n' || r == '\t' {
			cleaned.WriteRune(r)
			continue
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			continue
		}
		cleaned.WriteRune(r)
	}
	return cleaned.String()
}

func insertRunes(buffer []rune, cursor int, inserted []rune) []rune {
	buffer = append(buffer, make([]rune, len(inserted))...)
	copy(buffer[cursor+len(inserted):], buffer[cursor:len(buffer)-len(inserted)])
	copy(buffer[cursor:], inserted)
	return buffer
}

func containsNewline(buffer []rune) bool {
	for _, r := range buffer {
		if r == '\n' {
			return true
		}
	}
	return false
}

func lineStart(buffer []rune, cursor int) int {
	for cursor > 0 && buffer[cursor-1] != '\n' {
		cursor--
	}
	return cursor
}

func lineEnd(buffer []rune, cursor int) int {
	for cursor < len(buffer) && buffer[cursor] != '\n' {
		cursor++
	}
	return cursor
}

func moveVertical(buffer []rune, cursor, direction int) int {
	start := lineStart(buffer, cursor)
	column := cursor - start
	if direction < 0 {
		if start == 0 {
			return cursor
		}
		previousEnd := start - 1
		previousStart := lineStart(buffer, previousEnd)
		return previousStart + min(column, previousEnd-previousStart)
	}
	end := lineEnd(buffer, cursor)
	if end == len(buffer) {
		return cursor
	}
	nextStart := end + 1
	return nextStart + min(column, lineEnd(buffer, nextStart)-nextStart)
}

func (e *lineEditor) redraw(prefix string, buffer []rune, cursor int) error {
	width, _, err := term.GetSize(int(e.terminal.Fd()))
	if err != nil || width < cellWidth([]rune(prefix))+3 {
		width = 80
	}
	rows, cursorRow, cursorCol := editorRows(prefix, buffer, cursor, width)
	widthChanged := e.hasRenderedBuffer && e.lastWidth != width
	if e.hasRenderedBuffer {
		moveUp := e.lastCursorRow
		if widthChanged {
			moveUp = e.rowsToTopAfterResize(width)
		}
		if moveUp > 0 {
			if _, err := fmt.Fprintf(e.output, "\x1b[%dA", moveUp); err != nil {
				return err
			}
		}
		if _, err := io.WriteString(e.output, "\r\x1b[J"); err != nil {
			return err
		}
		if widthChanged {
			if _, err := fmt.Fprintf(e.output, "%s\r\n", strings.Repeat("─", width-1)); err != nil {
				return err
			}
		}
	} else {
		if _, err := fmt.Fprintf(e.output, "\r\n%s\r\n", strings.Repeat("─", width-1)); err != nil {
			return err
		}
	}
	for i, row := range rows {
		if i > 0 {
			if _, err := io.WriteString(e.output, "\r\n"); err != nil {
				return err
			}
		}
		if _, err := io.WriteString(e.output, row); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(e.output, "\r\n%s", strings.Repeat("─", width-1)); err != nil {
		return err
	}
	lastRow := len(rows)
	if lastRow > cursorRow {
		if _, err := fmt.Fprintf(e.output, "\x1b[%dA", lastRow-cursorRow); err != nil {
			return err
		}
	}
	if _, err := io.WriteString(e.output, "\r"); err != nil {
		return err
	}
	if cursorCol > 0 {
		if _, err := fmt.Fprintf(e.output, "\x1b[%dC", cursorCol); err != nil {
			return err
		}
	}
	e.lastCursorRow = cursorRow
	e.lastCursorCol = cursorCol
	e.lastWidth = width
	e.lastRowWidths = make([]int, len(rows))
	for i, row := range rows {
		e.lastRowWidths[i] = cellWidth([]rune(row))
	}
	e.lastRenderedRows = len(rows) + 1
	e.hasRenderedBuffer = true
	return nil
}

func (e *lineEditor) rowsToTopAfterResize(width int) int {
	if width < 1 {
		return e.lastCursorRow + 1
	}
	rows := wrappedRows(e.lastWidth-1, width)
	for i := 0; i < e.lastCursorRow && i < len(e.lastRowWidths); i++ {
		rows += wrappedRows(e.lastRowWidths[i], width)
	}
	rows += e.lastCursorCol / width
	return rows
}

func wrappedRows(cells, width int) int {
	if width < 1 || cells <= 0 {
		return 1
	}
	return (cells + width - 1) / width
}

func (e *lineEditor) endRenderedInput() error {
	if e.lastRenderedRows > 0 && e.lastRenderedRows-1 > e.lastCursorRow {
		if _, err := fmt.Fprintf(e.output, "\x1b[%dB", e.lastRenderedRows-1-e.lastCursorRow); err != nil {
			return err
		}
	}
	if _, err := io.WriteString(e.output, "\r\n"); err != nil {
		return err
	}
	e.lastCursorRow = 0
	e.lastCursorCol = 0
	e.lastWidth = 0
	e.lastRowWidths = nil
	e.lastRenderedRows = 0
	e.hasRenderedBuffer = false
	return nil
}

func editorRows(prefix string, buffer []rune, cursor, width int) ([]string, int, int) {
	indent := cellWidth([]rune(prefix))
	if indent >= width-1 {
		indent = 1
	}
	rows := []strings.Builder{{}}
	rowWidths := []int{0}
	appendText := func(text string, cells int) {
		row := len(rows) - 1
		rows[row].WriteString(text)
		rowWidths[row] += cells
	}
	rows[0].WriteString(prefix)
	rowWidths[0] = indent
	newRow := func() {
		rows = append(rows, strings.Builder{})
		rowWidths = append(rowWidths, 0)
		appendText(strings.Repeat(" ", indent), indent)
	}
	cursorRow, cursorCol := 0, indent
	for i, r := range buffer {
		if r == '\n' {
			if i == cursor {
				cursorRow, cursorCol = len(rows)-1, rowWidths[len(rowWidths)-1]
			}
			newRow()
			continue
		}
		currentCol := rowWidths[len(rowWidths)-1]
		cells := runeCellWidth(r, currentCol)
		if currentCol+cells > width-1 && currentCol > indent {
			newRow()
			currentCol = rowWidths[len(rowWidths)-1]
			cells = runeCellWidth(r, currentCol)
		}
		if i == cursor {
			cursorRow, cursorCol = len(rows)-1, currentCol
		}
		if r == '\t' {
			appendText(strings.Repeat(" ", cells), cells)
		} else {
			appendText(string(r), cells)
		}
	}
	if cursor == len(buffer) {
		cursorRow, cursorCol = len(rows)-1, rowWidths[len(rowWidths)-1]
	}
	result := make([]string, len(rows))
	for i := range rows {
		result[i] = rows[i].String()
	}
	return result, cursorRow, cursorCol
}

func cellWidth(text []rune) int {
	width := 0
	for _, r := range text {
		if unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) {
			continue
		}
		width += runeCellWidth(r, width)
	}
	return width
}

func runeCellWidth(r rune, column int) int {
	if r == '\t' {
		return 4 - column%4
	}
	if unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) {
		return 0
	}
	if r >= 0x1100 {
		return 2
	}
	return 1
}

func (e *lineEditor) addHistory(line string) {
	if len(e.history) == 0 || e.history[len(e.history)-1] != line {
		e.history = append(e.history, line)
	}
	if len(e.history) > 100 {
		e.history = e.history[len(e.history)-100:]
	}
}
