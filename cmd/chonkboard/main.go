// Command chonkboard is the whole application: one binary that serves the board,
// runs its own migrations, and needs nothing beside it but a writable directory.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/AzmainMahtab/chonkboard/internal/platform/config"
	"github.com/AzmainMahtab/chonkboard/internal/shared/logger"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "chonkboard:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := logger.New(string(cfg.Env), cfg.LogLevel)

	// One signal context for the whole process: the HTTP server drains on it, and
	// so will the backup ticker and every other long-lived goroutine.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if args := os.Args[1:]; len(args) > 0 {
		switch args[0] {
		case "migrate":
			return runMigrate(ctx, cfg, log, args[1:])
		case "seed":
			return runSeed(ctx, cfg, log, args[1:])
		default:
			return fmt.Errorf("unknown command %q (try: migrate, seed)", args[0])
		}
	}

	return serve(ctx, cfg, log)
}
