//go:build contexteval

package contextbaseline_test

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"go_im_gateway/internal/config"
	"go_im_gateway/internal/harness"
	"go_im_gateway/internal/harness/agent"
	"go_im_gateway/internal/harness/approval"
	"go_im_gateway/internal/harness/hooks"
	"go_im_gateway/internal/harness/sandbox"
	"go_im_gateway/internal/harness/session"
	"go_im_gateway/internal/harness/spill"
	"go_im_gateway/internal/harness/tokenmeter"
	"go_im_gateway/internal/workspace"
	"go_im_gateway/test/contextbaseline"
)

func TestContextBaselineReal(t *testing.T) {
	// This test is excluded from test-fast. Both tag and explicit script opt-in
	// are required before reading model configuration or touching Redis.
	if os.Getenv("CONTEXT_BASELINE_REAL") != "1" {
		t.Fatal("use scripts/context-baseline.sh")
	}
	c, err := contextbaseline.Load(os.Getenv("CONTEXT_BASELINE_CASE"), os.Getenv("CONTEXT_BASELINE_HOLDOUT") == "1")
	if err != nil {
		t.Fatal(err)
	}
	output := os.Getenv("CONTEXT_BASELINE_REPORT")
	if output == "" {
		t.Fatal("missing report path")
	}
	f, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	_, file, _, _ := runtime.Caller(0)
	repo := filepath.Clean(filepath.Join(filepath.Dir(file), "../.."))
	t.Chdir(repo)
	cfg := config.LoadConfig()
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	rdb := redis.NewClient(&redis.Options{Addr: cfg.Redis.Addr})
	defer rdb.Close()
	probe, stop := context.WithTimeout(ctx, 5*time.Second)
	err = rdb.Ping(probe).Err()
	stop()
	if err != nil {
		t.Fatal("Redis unavailable; no model called")
	}
	// Random evaluation owner, reserved independently of normal user 1. No DEL,
	// FLUSHDB or history copy. Existing candidate history/fold causes rejection.
	var raw [4]byte
	if _, err := rand.Read(raw[:]); err != nil {
		t.Fatal(err)
	}
	uid := uint(binary.BigEndian.Uint32(raw[:])%1_000_000_000 + 1_000_000_000)
	claimed, err := rdb.SetNX(ctx, fmt.Sprintf("context-baseline:owner:%d", uid), "reserved", 0).Result()
	if err != nil || !claimed {
		t.Fatal("could not reserve isolated evaluation owner")
	}
	n, err := rdb.Exists(ctx, fmt.Sprintf("agent:V2:history:%d", uid), fmt.Sprintf("agent:V2:fold:%d", uid)).Result()
	if err != nil || n != 0 {
		t.Fatal("evaluation owner is not empty; no model called")
	}
	session.Init(rdb)
	approval.Init(rdb)
	spill.Init(rdb)
	budget := tokenmeter.BudgetFromEnv()
	session.SetBudget(budget)
	hooks.RegisterDefaultHooks()
	// Only a synthetic fixture is readable; gold answers and personal files
	// are outside this tool root. MCP is deliberately not connected.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "project.md"), contextbaseline.Fixture(), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "reading.md"), contextbaseline.ReadingFixture(), 0600); err != nil {
		t.Fatal(err)
	}
	w, err := workspace.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	tools, err := w.Tools()
	if err != nil {
		t.Fatal(err)
	}
	rc, err := agent.RuntimeConfigFromEnv()
	if err != nil {
		t.Fatal("model configuration unavailable; check CLI configuration")
	}
	rc.ExtraTools = tools
	sourceHash := sourceDigest(t, repo)
	var h *harness.Harness
	restart := func(ctx context.Context) error {
		var err error
		h, err = harness.NewConfigured(ctx, rc, sandbox.FromEnv())
		return err
	}
	if err := restart(ctx); err != nil {
		t.Fatal(err)
	}
	report := contextbaseline.Run(ctx, uid, c, restart, func(ctx context.Context, input string, emit func(string)) (string, error) {
		return h.RunAgentTurn(ctx, uid, input, emit)
	})
	endpoint, _ := url.Parse(os.Getenv("VOLC_BASE_URL"))
	host := "default"
	if endpoint != nil && endpoint.Hostname() != "" {
		host = endpoint.Hostname()
	}
	report.Metadata = map[string]any{"owner": uid, "model": os.Getenv("VOLC_ENDPOINT_ID"), "provider_host": host,
		"go_version": runtime.Version(), "os": runtime.GOOS,
		"budget": budget, "max_steps": rc.MaxSteps, "retry_policy": rc.Retry, "source_sha256": sourceHash,
		"workspace": "synthetic project.md and reading.md only", "mcp": false, "turn_timeout_seconds": 180,
		"cache_assumption": "unknown; no artificial delay or cache flush", "restart": "new Harness, same process and Redis owner"}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(report); err != nil {
		t.Fatal(err)
	}
	if err := f.Sync(); err != nil {
		t.Fatal(err)
	}
	t.Logf("report=%s owner=%d turns=%d quality=unreviewed", output, uid, len(report.Turns))
	if !report.ExecutionComplete {
		t.Fatal("execution stopped; partial report saved")
	}
}

func sourceDigest(t *testing.T, repo string) string {
	t.Helper()
	h := sha256.New()
	for _, root := range []string{"internal", "test/contextbaseline", "go.mod", "go.sum"} {
		err := filepath.WalkDir(filepath.Join(repo, root), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			if !strings.HasSuffix(path, ".go") && root != "go.mod" && root != "go.sum" {
				return nil
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(repo, path)
			fmt.Fprintf(h, "%s\x00%d\x00", filepath.ToSlash(rel), len(b))
			h.Write(b)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}
