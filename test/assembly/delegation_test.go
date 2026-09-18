package assembly_test

import (
	"context"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/tool"
	"go_im_gateway/internal/harness/agent"
	"go_im_gateway/internal/harness/subagent"
)

func TestDelegationToolsKeepFactoriesSeparate(t *testing.T) {
	sets := make([][]tool.InvokableTool, 2)
	for i, name := range []string{"alpha", "beta"} {
		var err error
		sets[i], err = agent.NewDelegationTools(func(context.Context) (subagent.ChildAgent, error) {
			return dependencyLoop{text: name}, nil
		}, 2)
		if err != nil {
			t.Fatal(err)
		}
	}
	for i, name := range []string{"alpha", "beta"} {
		output, err := sets[i][0].InvokableRun(context.Background(), `{"task":"work"}`)
		if err != nil || output != name {
			t.Fatalf("single %s: output=%q err=%v", name, output, err)
		}
		output, err = sets[i][1].InvokableRun(context.Background(), `{"tasks":["first","second"]}`)
		if err != nil || strings.Count(output, name) != 2 || strings.Contains(output, "失败") {
			t.Fatalf("parallel %s: output=%q err=%v", name, output, err)
		}
	}
}

func TestDelegationToolsRejectMissingConfiguration(t *testing.T) {
	if _, err := agent.NewDelegationTools(nil, 1); err == nil {
		t.Fatal("nil runner accepted")
	}
	runner := subagent.Runner(func(context.Context) (subagent.ChildAgent, error) {
		return dependencyLoop{}, nil
	})
	for _, limit := range []int{0, -1} {
		if _, err := agent.NewDelegationTools(runner, limit); err == nil {
			t.Fatalf("invalid concurrency %d accepted", limit)
		}
	}
	var missing subagent.Runner
	if _, err := missing.Run(context.Background(), "work"); err == nil {
		t.Fatal("nil instance runner accepted")
	}
}
