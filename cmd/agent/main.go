package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"
	"go_im_gateway/internal/cli"
	"go_im_gateway/internal/config"
	"go_im_gateway/internal/harness"
	"go_im_gateway/internal/harness/agent"
	"go_im_gateway/internal/harness/approval"
	"go_im_gateway/internal/harness/hooks"
	"go_im_gateway/internal/harness/mcp"
	"go_im_gateway/internal/harness/sandbox"
	"go_im_gateway/internal/harness/session"
	"go_im_gateway/internal/harness/spill"
	"go_im_gateway/internal/harness/tokenmeter"
	"go_im_gateway/internal/workspace"
	"path/filepath"
)

func main() {
	if err := run(); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, "agent:", err)
		os.Exit(1)
	}
}

func run() error {
	userID := flag.Uint("user", 1, "local owner ID; reuses this user's existing Redis history")
	workspacePath := flag.String("workspace", ".", "root directory for read-only file tools")
	flag.Parse()
	if *userID == 0 || flag.NArg() != 0 {
		return fmt.Errorf("usage: gwagent [-user positive-ID] [-workspace directory]")
	}
	cfg := config.LoadConfig()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stop()
	rdb := redis.NewClient(&redis.Options{Addr: cfg.Redis.Addr})
	defer rdb.Close()
	probeCtx, probeCancel := context.WithTimeout(ctx, 5*time.Second)
	err := rdb.Ping(probeCtx).Err()
	probeCancel()
	if err != nil {
		return fmt.Errorf("Redis unavailable: %w", err)
	}
	session.Init(rdb)
	session.SetBudget(tokenmeter.BudgetFromEnv())
	approval.Init(rdb)
	spill.Init(rdb)
	hooks.RegisterDefaultHooks()
	if err := mcp.ConnectFromEnv(ctx); err != nil {
		return fmt.Errorf("configured MCP unavailable: %w", err)
	}
	directory, err := filepath.Abs(*workspacePath)
	if err != nil {
		return err
	}
	files, err := workspace.Open(directory)
	if err != nil {
		return err
	}
	defer files.Close()
	fileTools, err := files.Tools()
	if err != nil {
		return err
	}
	runtime, err := agent.RuntimeConfigFromEnv()
	if err != nil {
		return err
	}
	runtime.ExtraTools = append(runtime.ExtraTools, fileTools...)
	h, err := harness.NewConfigured(ctx, runtime, sandbox.FromEnv())
	if err != nil {
		return err
	}
	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt)
	defer signal.Stop(interrupts)
	if err := cli.Banner(os.Stdout, *userID, directory, fmt.Sprintf("%s (%s)", h.Exec.Describe(), h.Exec.Isolation())); err != nil {
		return err
	}
	return cli.Run(ctx, h, *userID, os.Stdin, os.Stdout, interrupts)
}
