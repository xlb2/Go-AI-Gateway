package e2e

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"go_im_gateway/internal/cli"
	"go_im_gateway/internal/harness"
	"go_im_gateway/internal/harness/session"
	"go_im_gateway/test/fakemodel"
)

func TestCLIReasoning(t *testing.T) {
	for i, mode := range []string{"on", "off", "absent"} {
		t.Run(mode, func(t *testing.T) {
			e := newEnv(t, uint(9150+i))
			reasoning := "provider reasoning fixture"
			if mode == "absent" {
				reasoning = ""
			}
			e.fake.SetScenario(fakemodel.Scenario{Default: fakemodel.Reply{Text: "final answer", Reasoning: reasoning}})
			h, err := harness.NewFromEnv(e.ctx())
			if err != nil {
				t.Fatal(err)
			}
			input := "hello\n/exit\n"
			if mode == "off" {
				input = "/reasoning off\n" + input
			}
			var out bytes.Buffer
			if err := cli.Run(e.ctx(), h, e.uid, io.NopCloser(strings.NewReader(input)), &out, nil); err != nil {
				t.Fatal(err)
			}
			text := out.String()
			if strings.Count(text, "final answer") != 1 {
				t.Fatalf("answer missing or repeated: %s", text)
			}
			if mode == "on" {
				if !strings.Contains(text, "Reasoning (provider) received\nAnswer\nfinal answer") || strings.Contains(text, reasoning) {
					t.Fatalf("reasoning not separated: %s", text)
				}
			} else if strings.Contains(text, "Reasoning (provider)") || strings.Contains(text, "provider reasoning fixture") {
				t.Fatalf("unexpected reasoning: %s", text)
			}
			history, err := h.Sessions.GetHistory(e.ctx(), e.uid)
			if err != nil {
				t.Fatal(err)
			}
			for _, msg := range history {
				if strings.Contains(msg.Content, "provider reasoning fixture") {
					t.Fatal("reasoning leaked into persisted answer")
				}
			}
		})
	}
}

func TestCLIToolProgress(t *testing.T) {
	e := newEnv(t, 9142)
	e.fake.SetScenario(fakemodel.DefaultScenario())
	h, err := harness.NewFromEnv(e.ctx())
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := cli.Run(e.ctx(), h, e.uid, io.NopCloser(strings.NewReader("查一下历史\n/exit\n")), &output, nil); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	if !strings.Contains(text, "Model attempts (main): 2 |") || !strings.Contains(text, "usage=2/2 input=") {
		t.Fatalf("missing per-attempt SDK accounting: %s", text)
	}
	if !strings.Contains(text, "Input estimate (main loop):") || !strings.Contains(text, "Usage (reported steps 2/2): input=") || !strings.Contains(text, "cache=unknown") {
		t.Fatalf("missing SDK input/usage accounting: %s", text)
	}
	if strings.Count(text, "Tools: 1 calls") != 1 || strings.Contains(text, "Tool   running") || strings.Contains(text, "Tool   returned") || strings.Contains(text, "Model  processing") {
		t.Fatalf("expected compact progress summary: %s", text)
	}
	counts := e.countTypes()
	if counts[session.EventToolCall] != 1 || counts[session.EventToolResult] != 1 {
		t.Fatalf("progress did not match persisted execution: %v", counts)
	}
}

func TestCLIHarnessHistory(t *testing.T) {
	e := newEnv(t, 9141)
	e.fake.SetScenario(fakemodel.Scenario{Default: fakemodel.Reply{Text: "terminal reply"}})
	h, err := harness.NewFromEnv(e.ctx())
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := cli.Run(e.ctx(), h, e.uid, io.NopCloser(strings.NewReader("first terminal input\nsecond terminal input\n/exit\n")), &output, nil); err != nil {
		t.Fatal(err)
	}
	if strings.Count(output.String(), "terminal reply") != 2 {
		t.Fatalf("duplicated or missing output: %s", output.String())
	}
	counts := e.countTypes()
	if counts[session.EventStepStart] != 2 || counts[session.EventTurnEnd] != 2 {
		t.Fatalf("missing Harness events: %v", counts)
	}
	history, err := h.Sessions.GetHistory(e.ctx(), e.uid)
	if err != nil {
		t.Fatal(err)
	}
	var contents []string
	for _, msg := range history {
		contents = append(contents, msg.Content)
	}
	for _, want := range []string{"first terminal input", "second terminal input"} {
		if !strings.Contains(strings.Join(contents, "\n"), want) {
			t.Fatalf("history missing %q: %v", want, contents)
		}
	}
	if len(e.fake.Requests()) != 2 {
		t.Fatalf("unexpected model requests: %d", len(e.fake.Requests()))
	}
}
