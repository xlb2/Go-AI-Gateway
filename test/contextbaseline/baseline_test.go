package contextbaseline_test

import (
	"context"
	"errors"
	"testing"

	"go_im_gateway/test/contextbaseline"
)

func TestContextBaselineFixtures(t *testing.T) {
	c, err := contextbaseline.Load("resume-dev", false)
	if err != nil || len(c.Turns) != 3 || !c.Turns[1].RestartBefore {
		t.Fatalf("case=%+v err=%v", c, err)
	}
	if _, err := contextbaseline.Load("resume-holdout", false); err == nil {
		t.Fatal("holdout ran without explicit selection")
	}
	if _, err := contextbaseline.Load("missing", true); err == nil {
		t.Fatal("unknown case accepted")
	}
}

func TestContextBaselineStopsOnFailure(t *testing.T) {
	c, err := contextbaseline.Load("resume-dev", false)
	if err != nil {
		t.Fatal(err)
	}
	var calls, restarts int
	report := contextbaseline.Run(context.Background(), 42, c, func(ctx context.Context) error {
		if ctx.Value("user_id") != uint(42) {
			t.Fatal("restart lost owner")
		}
		restarts++
		return nil
	},
		func(ctx context.Context, input string, emit func(string)) (string, error) {
			if ctx.Value("user_id") != uint(42) {
				t.Fatal("turn lost owner")
			}
			calls++
			if calls == 2 {
				emit("partial")
				return "partial", errors.New("private provider detail")
			}
			emit("ok")
			return "ok", nil
		})
	if calls != 2 || restarts != 1 || len(report.Turns) != 2 || report.ExecutionComplete || report.Quality != "unreviewed" {
		t.Fatalf("failed sequence continued or marked passed: %+v", report)
	}
	if report.Turns[1].Status != "error" || report.Turns[1].Reply != "partial" || report.Turns[1].FirstTextMS == nil {
		t.Fatalf("failure evidence missing: %+v", report.Turns[1])
	}
	if report.Fees != nil || report.Cache != nil {
		t.Fatal("unknown fees/cache invented")
	}
	if report.Turns[1].ErrorCode != "run_error" {
		t.Fatalf("unsafe or missing error classification: %s", report.Turns[1].ErrorCode)
	}
}
