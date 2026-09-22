package cli

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode"

	"go_im_gateway/internal/harness/agent"

	"github.com/mattn/go-isatty"
	"golang.org/x/term"
)

type display struct {
	out             io.Writer
	color           bool
	terminal        *os.File
	reasoningTail   []rune
	reasoningActive bool
}

func newDisplay(out io.Writer) display {
	f, ok := out.(*os.File)
	_, noColor := os.LookupEnv("NO_COLOR")
	d := display{out: out}
	if ok && isatty.IsTerminal(f.Fd()) && os.Getenv("TERM") != "dumb" {
		d.terminal = f
		d.color = !noColor
	}
	return d
}

// Keep only a bounded tail, with conservative two-cell budgeting per rune.
// This also leaves room for wide CJK characters without splitting UTF-8.
func (d *display) reasoning(chunk string) error {
	d.reasoningActive = true
	for _, r := range chunk {
		if unicode.IsSpace(r) {
			r = ' '
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			continue
		}
		d.reasoningTail = append(d.reasoningTail, r)
		if len(d.reasoningTail) > 120 {
			copy(d.reasoningTail, d.reasoningTail[len(d.reasoningTail)-120:])
			d.reasoningTail = d.reasoningTail[:120]
		}
	}
	if d.terminal == nil {
		return nil
	}
	width, _, err := term.GetSize(int(d.terminal.Fd()))
	if err != nil || width < 2 {
		return nil
	}
	label := "Reasoning > "
	if len(label) >= width {
		label = ">"
	}
	count := (width - len(label) - 1) / 2
	tail := d.reasoningTail
	if len(tail) > count {
		tail = tail[len(tail)-count:]
	}
	_, err = fmt.Fprint(d.out, "\r\x1b[2K"+d.style("2", label+string(tail)))
	return err
}

func (d *display) finishReasoning() error {
	if !d.reasoningActive {
		return nil
	}
	d.reasoningActive = false
	d.reasoningTail = nil
	if d.terminal != nil {
		_, err := io.WriteString(d.out, "\r\x1b[2K")
		return err
	}
	_, err := fmt.Fprintln(d.out, "Reasoning (provider) received")
	return err
}

func (d display) style(code, text string) string {
	if !d.color {
		return text
	}
	return "\x1b[" + code + "m" + text + "\x1b[0m"
}

// Banner shows local runtime metadata, never credentials or endpoint URLs.
func Banner(out io.Writer, userID uint, directory, isolation string) error {
	d := newDisplay(out)
	clean := func(s string) string {
		return strings.Map(func(r rune) rune {
			if unicode.IsControl(r) {
				return -1
			}
			return r
		}, s)
	}
	_, err := fmt.Fprintf(out, "\n  %s  %s\n\n  user       %d\n  workspace  %s\n  isolation  %s\n\n  %s\n",
		d.style("1;36", "GO AGENT"), d.style("2", "CLI"), userID, clean(directory), clean(isolation),
		d.style("2", "/help  /exit  |  Ctrl+C cancel / exit"))
	return err
}

func (d display) prompt() error {
	_, err := fmt.Fprint(d.out, "\n"+d.style("1;32", "You")+"\n"+d.style("32", "> "))
	return err
}

func (d display) begin() error {
	_, err := fmt.Fprint(d.out, "\n"+d.style("1;36", "Agent")+"\n"+d.style("2", "Working...")+"\n")
	return err
}

func (d display) clearWaiting() error {
	if !d.color {
		return nil
	}
	_, err := io.WriteString(d.out, "\x1b[1A\r\x1b[2K")
	return err
}

func (d display) status(label, color string, elapsed time.Duration) error {
	_, err := fmt.Fprintf(d.out, "\n%s\n", d.style(color, fmt.Sprintf("%s  %.1fs", label, elapsed.Seconds())))
	return err
}

func (d display) progress(event agent.Progress) error {
	label, color := "", "2"
	switch event.Kind {
	case agent.ProgressModel:
		label = "Model  processing..."
	case agent.ProgressToolStart:
		label, color = "Tool   running   ", "36"
	case agent.ProgressToolReturned:
		label = "Tool   returned  "
	case agent.ProgressToolFailed:
		label, color = "Tool   failed / unavailable  ", "33"
	default:
		return nil
	}
	// Tool names are external metadata; never let them control the terminal.
	name := []rune(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, event.Tool))
	if len(name) > 80 {
		name = name[:80]
	}
	_, err := fmt.Fprintln(d.out, d.style(color, label+string(name)))
	return err
}
