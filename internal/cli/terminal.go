// Package cli adapts terminal input and output to the existing Harness.
package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"go_im_gateway/internal/harness"
	"go_im_gateway/internal/harness/agent"
)

// Runner is the existing Harness capability consumed by the terminal.
type Runner interface {
	RunAgentTurn(context.Context, uint, string, func(string)) (string, error)
	HandleApprovalCommand(context.Context, uint, string) (bool, string)
}

// Run owns input and closes it on return. A runner must honor context cancellation.
// Interrupt cancels an active operation and waits for its return; at a prompt it exits.
func Run(ctx context.Context, runner Runner, userID uint, input io.ReadCloser, output io.Writer, interrupts <-chan os.Signal) error {
	defer input.Close()
	if runner == nil || userID == 0 {
		return fmt.Errorf("CLI requires a runner and a positive user ID")
	}
	ctx = context.WithValue(ctx, "user_id", userID)
	display := newDisplay(output)
	showReasoning := true
	pasteMode := false
	var paste strings.Builder
	readCtx, stopReading := context.WithCancel(ctx)
	defer stopReading()
	type line struct {
		text string
		err  error
	}
	lines := make(chan line)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(input)
		scanner.Buffer(make([]byte, 4096), 64*1024)
		for scanner.Scan() {
			select {
			case lines <- line{text: scanner.Text()}:
			case <-readCtx.Done():
				return
			}
		}
		if err := scanner.Err(); err != nil {
			select {
			case lines <- line{err: err}:
			case <-readCtx.Done():
			}
		}
	}()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if pasteMode {
			if _, err := io.WriteString(output, "... "); err != nil {
				return err
			}
		} else {
			if err := display.prompt(); err != nil {
				return err
			}
		}
		var next line
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-interrupts:
			return nil
		case value, ok := <-lines:
			if !ok {
				return nil
			}
			next = value
		}
		if next.err != nil {
			return fmt.Errorf("read input: %w", next.err)
		}
		text := strings.TrimSpace(next.text)
		pasted := false
		if pasteMode {
			switch text {
			case "/cancel":
				paste.Reset()
				pasteMode = false
				continue
			case "/send":
				text = strings.TrimSuffix(paste.String(), "\n")
				paste.Reset()
				pasteMode = false
				pasted = true
			default:
				if paste.Len()+len(next.text)+1 > 64*1024 {
					return fmt.Errorf("paste exceeds 64 KiB; nothing sent")
				}
				paste.WriteString(next.text)
				paste.WriteByte('\n')
				continue
			}
		}
		if strings.TrimSpace(text) == "" {
			continue
		}
		if !pasted {
			switch text {
			case "/paste":
				pasteMode = true
				if _, err := fmt.Fprintln(output, "Paste mode: /send submits, /cancel discards (on a separate line)."); err != nil {
					return err
				}
				continue
			case "/reasoning on", "/reasoning off":
				showReasoning = text == "/reasoning on"
				if _, err := fmt.Fprintf(output, "Reasoning display: %s\n", strings.TrimPrefix(text, "/reasoning ")); err != nil {
					return err
				}
				continue
			case "/exit":
				return nil
			case "/help":
				if _, err := fmt.Fprintln(output, "\nCommands\n  /help                Show commands\n  /exit                Exit\n  /paste               Collect multiline input; /send submits, /cancel discards\n  /reasoning on|off    Show / hide provider reasoning\n  auth:approve [code]   Review / confirm approval\n  auth:reject          Reject pending action\n  auth:status <id>     Check action status\n\nCtrl+C cancels an active turn; exits when idle."); err != nil {
					return err
				}
				continue
			}
			if strings.HasPrefix(text, "/") {
				if _, err := fmt.Fprintln(output, "Unknown command. /help"); err != nil {
					return err
				}
				continue
			}
		}
		if err := display.begin(); err != nil {
			return err
		}
		started := time.Now()
		runCtx, cancel := context.WithCancel(ctx)
		type result struct{ runErr, writeErr error }
		done := make(chan result, 1)
		go func() {
			var outputMu sync.Mutex
			var writeErr error
			streamed := false
			waiting := true
			textOpen := false
			closed := false
			section := ""
			runCtx := agent.WithProgress(runCtx, func(event agent.Progress) {
				outputMu.Lock()
				defer outputMu.Unlock()
				if closed || writeErr != nil {
					return
				}
				writeErr = display.finishReasoning()
				if writeErr != nil {
					cancel()
					return
				}
				if waiting {
					waiting = false
					writeErr = display.clearWaiting()
				}
				if writeErr == nil && textOpen {
					_, writeErr = io.WriteString(output, "\n")
					textOpen = false
				}
				if writeErr == nil {
					writeErr = display.progress(event)
					section = ""
				}
				if writeErr != nil {
					cancel()
				}
			})
			emitSection := func(kind, chunk string) {
				outputMu.Lock()
				defer outputMu.Unlock()
				if closed || chunk == "" || writeErr != nil {
					return
				}
				writeErr = display.clearProgress()
				if writeErr != nil {
					cancel()
					return
				}
				if kind == "answer" {
					writeErr = display.finishReasoning()
					if writeErr != nil {
						cancel()
						return
					}
				}
				if waiting {
					waiting = false
					writeErr = display.clearWaiting()
					if writeErr != nil {
						cancel()
						return
					}
				}
				if kind == "reasoning" {
					if textOpen {
						_, writeErr = io.WriteString(output, "\n")
						textOpen = false
					}
					if writeErr == nil {
						writeErr = display.reasoning(chunk)
					}
					section = kind
					if writeErr != nil {
						cancel()
					}
					return
				}
				if section != kind && section == "reasoning" {
					if textOpen {
						_, writeErr = io.WriteString(output, "\n")
					}
					label := "Answer"
					if writeErr == nil {
						_, writeErr = fmt.Fprintln(output, display.style("2", label))
					}
					if writeErr != nil {
						cancel()
						return
					}
				}
				section = kind
				if kind == "answer" {
					streamed = true
				}
				textOpen = !strings.HasSuffix(chunk, "\n")
				_, writeErr = io.WriteString(output, chunk)
				if writeErr != nil {
					cancel()
				}
			}
			emit := func(chunk string) { emitSection("answer", chunk) }
			runCtx = harness.WithReasoning(runCtx, func(chunk string) {
				if showReasoning {
					emitSection("reasoning", chunk)
				}
			})
			// Pasted commands are message content, never approval actions.
			var handled bool
			var reply string
			if !pasted {
				handled, reply = runner.HandleApprovalCommand(runCtx, userID, text)
			}
			var err error
			if !handled {
				reply, err = runner.RunAgentTurn(runCtx, userID, text, emit)
			}
			// Pre-hooks and approval commands can return a reply without streaming it.
			if !streamed {
				emit(reply)
			}
			outputMu.Lock()
			closed = true
			if writeErr == nil {
				writeErr = display.clearProgress()
			}
			if writeErr == nil {
				writeErr = display.finishReasoning()
			}
			if waiting && writeErr == nil {
				writeErr = display.clearWaiting()
			}
			outputMu.Unlock()
			done <- result{err, writeErr}
		}()
		var outcome result
		select {
		case outcome = <-done:
		case <-interrupts:
			cancel()
			outcome = <-done
		case <-ctx.Done():
			cancel()
			<-done
			return ctx.Err()
		}
		wasCanceled := runCtx.Err() != nil
		cancel()
		if outcome.writeErr != nil {
			return outcome.writeErr
		}
		if _, err := fmt.Fprintln(output); err != nil {
			return err
		}
		if err := display.toolSummary(); err != nil {
			return err
		}
		if wasCanceled || errors.Is(outcome.runErr, context.Canceled) {
			if err := display.status("Canceled", "33", time.Since(started)); err != nil {
				return err
			}
		} else if outcome.runErr == nil {
			if err := display.status("Done", "2", time.Since(started)); err != nil {
				return err
			}
		}
		if outcome.runErr != nil {
			if _, err := fmt.Fprintf(output, "%s %v\n", display.style("1;31", "Error:"), outcome.runErr); err != nil {
				return err
			}
		}
	}
}
