package e2e

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"go_im_gateway/internal/cli"
	"go_im_gateway/internal/harness"
	"go_im_gateway/test/fakemodel"
)

func TestCLIContextObservationAcrossTurns(t *testing.T) {
	e := newEnv(t, 9172)
	e.fake.SetScenario(fakemodel.Scenario{Default: fakemodel.Reply{Text: "answer"}})
	h, err := harness.NewFromEnv(e.ctx())
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := cli.Run(e.ctx(), h, e.uid, io.NopCloser(strings.NewReader("first\nsecond\n/exit\n")), &output, nil); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	if strings.Count(text, "Model attempts (main): 1 |") != 2 || strings.Count(text, "usage=1/1 input=") != 2 {
		t.Fatalf("SDK attempt accounting did not reset: %s", text)
	}
	if strings.Count(text, "Input estimate (main loop):") != 2 || strings.Count(text, "Usage (reported steps 1/1): input=") != 2 {
		t.Fatalf("per-turn accounting did not reset: %s", text)
	}
	requests := e.fake.Requests()
	if len(requests) != 2 {
		t.Fatalf("requests=%d", len(requests))
	}
	found := false
	for _, req := range requests {
		for _, msg := range req.Messages {
			if strings.Contains(msg.Content, "Input estimate (main loop):") || strings.Contains(msg.Content, "Usage (reported steps") {
				t.Fatal("diagnostics fed back to model")
			}
		}
	}
	for _, msg := range requests[1].Messages {
		if msg.Role == "user" && msg.Content == "first" {
			found = true
		}
	}
	if !found {
		t.Fatal("observation changed history selection")
	}
}
