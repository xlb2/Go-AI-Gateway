package assembly_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"go_im_gateway/internal/harness/guard"
)

type approvalGuardProbe struct {
	calls  int
	wait   bool
	fail   bool
	output string
}

func (*approvalGuardProbe) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: "mcp__fixture__read"}, nil
}
func (p *approvalGuardProbe) InvokableRun(ctx context.Context, _ string, _ ...tool.Option) (string, error) {
	p.calls++
	if p.wait {
		<-ctx.Done()
		return "", ctx.Err()
	}
	if p.fail {
		return "", errors.New(p.output)
	}
	return p.output, nil
}

func TestToolApprovalGuardRetainsRestrictions(t *testing.T) {
	ctx := context.Background()
	p, err := guard.NewConfiguredPipeline(guard.Config{Timeout: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	secret := "sk-" + strings.Repeat("a", 24)
	probe := &approvalGuardProbe{output: secret}
	if _, err := p.Wrap(probe).(tool.InvokableTool).InvokableRun(ctx, "{}"); err != nil || probe.calls != 0 {
		t.Fatal("unapproved MCP tool executed")
	}
	out, err := p.RunApproved(ctx, probe, "{}")
	if err != nil || probe.calls != 1 || strings.Contains(out, secret) {
		t.Fatalf("out=%q err=%v", out, err)
	}
	probe.fail = true
	if _, err := p.RunApproved(ctx, probe, "{}"); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("unsafe error: %v", err)
	}
	probe.wait = true
	if _, err := p.RunApproved(ctx, probe, "{}"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout: %v", err)
	}
	before := probe.calls
	if _, err := p.RunApproved(ctx, probe, strings.Repeat("x", 8193)); err == nil || probe.calls != before {
		t.Fatal("approval bypassed argument limit")
	}
	p.Guard(func(context.Context, guard.Call) guard.Verdict {
		return guard.Verdict{Decision: guard.Deny, Reason: "blocked"}
	})
	if _, err := p.RunApproved(ctx, probe, "{}"); err == nil || probe.calls != before {
		t.Fatal("approval bypassed Deny")
	}
}
