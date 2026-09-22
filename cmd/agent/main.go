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
	"go_im_gateway/internal/harness/approval"
	"go_im_gateway/internal/harness/hooks"
	"go_im_gateway/internal/harness/mcp"
	"go_im_gateway/internal/harness/session"
	"go_im_gateway/internal/harness/spill"
	"go_im_gateway/internal/harness/tokenmeter"
)

func main() {
	if err := run(); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, "agent:", err)
		os.Exit(1)
	}
}

func run() error {
	userID := flag.Uint("user", 1, "local owner ID; reuses this user's existing Redis history")
	flag.Parse()
	if *userID == 0 || flag.NArg() != 0 {
		return fmt.Errorf("usage: gwagent [-user positive-ID]")
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
	h, err := harness.NewFromEnv(ctx)
	if err != nil {
		return err
	}
	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt)
	defer signal.Stop(interrupts)
	directory, err := os.Getwd()
	if err != nil {
		return err
	}
	if err := cli.Banner(os.Stdout, *userID, directory, fmt.Sprintf("%s (%s)", h.Exec.Describe(), h.Exec.Isolation())); err != nil {
		return err
	}
	return cli.Run(ctx, h, *userID, os.Stdin, os.Stdout, interrupts)
}
