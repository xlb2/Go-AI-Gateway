package cli_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"go_im_gateway/internal/cli"
)

type runner struct {
	run     func(context.Context, uint, string, func(string)) (string, error)
	approve func(context.Context, uint, string) (bool, string)
}

func (r runner) RunAgentTurn(ctx context.Context, uid uint, input string, emit func(string)) (string, error) {
	return r.run(ctx, uid, input, emit)
}

func (r runner) HandleApprovalCommand(ctx context.Context, uid uint, input string) (bool, string) {
	if r.approve != nil {
		return r.approve(ctx, uid, input)
	}
	return false, ""
}

func TestCLIPaste(t *testing.T) {
	for _, tc := range []struct {
		name, input, want string
		wantErr           bool
	}{
		{"paragraphs", "/paste\r\nfirst\r\n\r\nsecond\r\nthird\r\n/send\r\n/exit\r\n", "first\n\nsecond\nthird", false},
		{"commands_are_text", "/paste\n/exit\nauth:approve 1234\n/send\n/exit\n", "/exit\nauth:approve 1234", false},
		{"approval_is_text", "/paste\nauth:reject\n/send\n/exit\n", "auth:reject", false},
		{"cancel", "/paste\nfirst\n/cancel\n/exit\n", "", false},
		{"empty", "/paste\n/send\n/exit\n", "", false},
		{"eof_discards", "/paste\nfirst\nsecond\n", "", false},
		{"bounded", "/paste\n" + strings.Repeat(strings.Repeat("x", 1024)+"\n", 65), "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			var inputs []string
			r := runner{
				run: func(_ context.Context, _ uint, input string, _ func(string)) (string, error) {
					inputs = append(inputs, input)
					return "ok", nil
				},
				approve: func(context.Context, uint, string) (bool, string) {
					t.Error("pasted content dispatched as approval command")
					return false, ""
				},
			}
			err := cli.Run(context.Background(), r, 1, io.NopCloser(strings.NewReader(tc.input)), &out, nil)
			if (err != nil) != tc.wantErr {
				t.Fatalf("error: %v", err)
			}
			if tc.want == "" {
				if len(inputs) != 0 {
					t.Fatalf("unexpected turns: %q", inputs)
				}
			} else if len(inputs) != 1 || inputs[0] != tc.want {
				t.Fatalf("turns: %q, want one %q", inputs, tc.want)
			}
		})
	}
}

func TestCLIConversationAndApproval(t *testing.T) {
	var out bytes.Buffer
	var inputs []string
	r := runner{
		run: func(ctx context.Context, uid uint, input string, emit func(string)) (string, error) {
			if uid != 7 || ctx.Value("user_id") != uint(7) {
				return "", errors.New("wrong owner")
			}
			inputs = append(inputs, input)
			if input == "first" {
				emit("streamed")
				return "streamed", nil
			}
			return "blocked by hook", nil
		},
		approve: func(_ context.Context, _ uint, input string) (bool, string) {
			return input == "auth:reject", "rejected"
		},
	}
	err := cli.Run(context.Background(), r, 7, io.NopCloser(strings.NewReader("\nfirst\nauth:reject\nsecond\n/exit\nignored\n")), &out, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(inputs, ",") != "first,second" {
		t.Fatalf("inputs: %v", inputs)
	}
	for _, want := range []string{"streamed", "rejected", "blocked by hook"} {
		if strings.Count(out.String(), want) != 1 {
			t.Fatalf("expected %q once: %s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "\x1b") {
		t.Fatal("redirected output contains terminal escape sequences")
	}
}

func TestCLIBanner(t *testing.T) {
	var out bytes.Buffer
	if err := cli.Banner(&out, 7, "/workspace/\x1b[2Jproject\n", "local (none)"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"GO AGENT", "7", "local (none)", "/help"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q: %s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "\x1b") || strings.Contains(out.String(), "project\n\n") {
		t.Fatalf("metadata contains control characters: %q", out.String())
	}
	if err := cli.Banner(failedWriter{}, 1, "/workspace", "none"); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("banner output error: %v", err)
	}
}

func TestCLIErrorAndCommands(t *testing.T) {
	var out bytes.Buffer
	calls := 0
	r := runner{run: func(_ context.Context, _ uint, input string, emit func(string)) (string, error) {
		calls++
		if input == "fail" {
			emit("partial")
			return "partial", errors.New("upstream failed")
		}
		return "next answer", nil
	}}
	err := cli.Run(context.Background(), r, 1, io.NopCloser(strings.NewReader("/help\n/unknown\nfail\nnext\n")), &out, nil)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || strings.Count(out.String(), "partial") != 1 || !strings.Contains(out.String(), "upstream failed") || !strings.Contains(out.String(), "next answer") {
		t.Fatalf("calls=%d output=%s", calls, out.String())
	}
}

func TestCLICancelWaitsBeforeNextTurn(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	parent := ctx
	signals := make(chan os.Signal, 1)
	started, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	second := make(chan struct{}, 1)
	r := runner{run: func(ctx context.Context, _ uint, input string, _ func(string)) (string, error) {
		if input == "first" {
			close(started)
			<-ctx.Done()
			close(canceled)
			select {
			case <-release:
			case <-parent.Done():
			}
			return "", ctx.Err()
		}
		second <- struct{}{}
		return "continued", nil
	}}
	var out bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- cli.Run(ctx, r, 1, io.NopCloser(strings.NewReader("first\nsecond\n/exit\n")), &out, signals)
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("first turn did not start")
	}
	signals <- os.Interrupt
	select {
	case <-canceled:
	case <-ctx.Done():
		t.Fatal("cancel not propagated")
	}
	select {
	case <-second:
		t.Fatal("next turn began before cleanup")
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("CLI did not finish")
	}
	select {
	case <-second:
	default:
		t.Fatal("CLI did not continue after cancel")
	}
}

type failedWriter struct{}

func (failedWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestCLIInputAndOutputFailures(t *testing.T) {
	r := runner{run: func(context.Context, uint, string, func(string)) (string, error) {
		return "", errors.New("unexpected run")
	}}
	if err := cli.Run(context.Background(), r, 1, io.NopCloser(strings.NewReader("")), failedWriter{}, nil); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("output error: %v", err)
	}
	var out bytes.Buffer
	if err := cli.Run(context.Background(), r, 1, io.NopCloser(strings.NewReader(strings.Repeat("x", 128*1024))), &out, nil); err == nil {
		t.Fatal("oversized input accepted")
	}
	signals := make(chan os.Signal, 1)
	signals <- os.Interrupt
	reader, writer := io.Pipe()
	defer writer.Close()
	if err := cli.Run(context.Background(), r, 1, reader, &out, signals); err != nil {
		t.Fatal(err)
	}
}
