// Initdb creates the Spanner database and applies schema.sql on the emulator.
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"

	database "cloud.google.com/go/spanner/admin/database/apiv1"
	databasepb "cloud.google.com/go/spanner/admin/database/apiv1/databasepb"

	"github.com/justabill-org/justabill/db/schema"
	"github.com/justabill-org/justabill/obs"
)

func main() {
	os.Exit(obs.Main(context.Background(), obs.Config{Service: "justabill-db", Environment: os.Getenv("APP_ENV")},
		func(ctx context.Context, logger *slog.Logger) error { return run(ctx, logger, os.Stdout) }))
}

func run(ctx context.Context, logger *slog.Logger, w io.Writer) error {
	adminClient, err := database.NewDatabaseAdminClient(ctx)
	if err != nil {
		return fmt.Errorf("admin client: %w", err)
	}
	defer adminClient.Close()

	stmts, err := schema.Read()
	if err != nil {
		return fmt.Errorf("read schema.sql: %w", err)
	}
	logger.InfoContext(ctx, "parsed schema", "statements", len(stmts))

	project := envOr("SPANNER_PROJECT", "justabill-local")
	instance := envOr("SPANNER_INSTANCE", "test-instance")
	dbName := envOr("SPANNER_DATABASE", "justabill")

	parent := fmt.Sprintf("projects/%s/instances/%s", project, instance)
	op, err := adminClient.CreateDatabase(ctx, &databasepb.CreateDatabaseRequest{
		Parent:          parent,
		CreateStatement: fmt.Sprintf("CREATE DATABASE `%s`", dbName),
		ExtraStatements: stmts,
	})
	if err != nil {
		return fmt.Errorf("create database: %w", err)
	}

	db, err := op.Wait(ctx)
	if err != nil {
		return fmt.Errorf("wait: %w", err)
	}
	_, err = fmt.Fprintf(w, "Created database: %s\n", db.GetName())
	return err
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
