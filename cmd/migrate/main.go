package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/wgdl666/wgModelHub/config"
	"github.com/wgdl666/wgModelHub/internal/dbmigration"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "migration failed: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	loader, err := config.NewRuntimeLoaderFromEnv()
	if err != nil {
		return fmt.Errorf("initialize runtime configuration: %w", err)
	}
	defer loader.Close()
	runtimeConfig, _, err := loader.Load(ctx)
	if err != nil {
		return fmt.Errorf("load runtime configuration: %w", err)
	}

	db, err := sql.Open("pgx", runtimeConfig.Database.DSN)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("ping database: %w", err)
	}
	if err := dbmigration.Run(ctx, db); err != nil {
		return fmt.Errorf("apply generation task migration: %w", err)
	}
	return nil
}
