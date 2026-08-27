package main

import (
	"context"
	"database/sql"
	"flag"
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
	if err := run(ctx, os.Args[1:]); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "migration failed: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("wg-model-hub-migrate", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	validateConfigOnly := flags.Bool("validate-config-only", false, "validate runtime configuration without connecting to the database")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("parse migration arguments")
	}
	loader, err := config.NewRuntimeLoaderFromEnv()
	if err != nil {
		return fmt.Errorf("initialize runtime configuration")
	}
	defer loader.Close()
	runtimeConfig, _, err := loader.Load(ctx)
	if err != nil {
		return fmt.Errorf("load runtime configuration")
	}
	if *validateConfigOnly {
		return nil
	}

	db, err := sql.Open("pgx", runtimeConfig.Database.DSN)
	if err != nil {
		return fmt.Errorf("open database")
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("ping database")
	}
	if err := dbmigration.Run(ctx, db); err != nil {
		return fmt.Errorf("apply generation task migration")
	}
	return nil
}
