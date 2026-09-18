package assembly_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"go_im_gateway/internal/harness/guard"
)

type guardedProbe struct {
	name      string
	calls     int
	remaining time.Duration
}

func (p *guardedProbe) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: p.name}, nil
}
func (p *guardedProbe) InvokableRun(ctx context.Context, _ string, _ ...tool.Option) (string, error) {
	p.calls++
	deadline, _ := ctx.Deadline()
	p.remaining = time.Until(deadline)
	return "executed", nil
}

func TestConfiguredGuardPoliciesAreIsolated(t *testing.T) {
	cfg := guard.Config{RequiredApprovals: map[string]string{"local": "confirm"}, TrustedExternalServers: map[string]bool{"server": true}}
	a, err := guard.NewConfiguredPipeline(cfg)
	if err != nil {
		t.Fatal(err)
	}
	b, err := guard.NewConfiguredPipeline(guard.Config{})
	if err != nil {
		t.Fatal(err)
	}
	delete(cfg.RequiredApprovals, "local")
	cfg.TrustedExternalServers["server"] = false
	for _, tc := range []struct {
		p        *guard.Pipeline
		name     string
		executes bool
	}{
		{a, "local", false}, {b, "local", true},
		{a, "mcp__server__read", true}, {b, "mcp__server__read", false},
	} {
		probe := &guardedProbe{name: tc.name}
		out, err := tc.p.Wrap(probe).(tool.InvokableTool).InvokableRun(context.Background(), "{}")
		if err != nil {
			t.Fatal(err)
		}
		if (probe.calls == 1) != tc.executes {
			t.Fatalf("%s calls=%d", tc.name, probe.calls)
		}
		if !tc.executes && !strings.Contains(out, "fail-closed") {
			t.Fatalf("missing closed rejection: %q", out)
		}
	}
}

func TestConfiguredGuardTimeoutsAreIsolated(t *testing.T) {
	cfg := guard.Config{Timeout: time.Hour, ToolTimeouts: map[string]time.Duration{"probe": 2 * time.Hour}}
	a, err := guard.NewConfiguredPipeline(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ToolTimeouts["probe"] = 3 * time.Hour
	b, err := guard.NewConfiguredPipeline(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		p        *guard.Pipeline
		name     string
		duration time.Duration
	}{
		{a, "probe", 2 * time.Hour}, {b, "probe", 3 * time.Hour}, {a, "other", time.Hour},
	} {
		probe := &guardedProbe{name: tc.name}
		_, err := tc.p.Wrap(probe).(tool.InvokableTool).InvokableRun(context.Background(), "{}")
		if err != nil {
			t.Fatal(err)
		}
		if probe.remaining > tc.duration || probe.remaining < tc.duration-time.Minute {
			t.Fatalf("deadline remaining=%s want approximately %s", probe.remaining, tc.duration)
		}
	}
}

func TestConfiguredGuardRejectsInvalidConfiguration(t *testing.T) {
	for _, cfg := range []guard.Config{
		{Timeout: -time.Second},
		{ToolTimeouts: map[string]time.Duration{"probe": 0}},
		{ToolTimeouts: map[string]time.Duration{"": time.Second}},
		{RequiredApprovals: map[string]string{"": "reason"}},
		{TrustedExternalServers: map[string]bool{"": true}},
	} {
		if _, err := guard.NewConfiguredPipeline(cfg); err == nil {
			t.Fatalf("accepted invalid config: %+v", cfg)
		}
	}
}
