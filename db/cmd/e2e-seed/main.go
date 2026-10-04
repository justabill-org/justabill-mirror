// e2e-seed builds a fresh emulator database for the browser smoke tests
// (docs/design/84-e2e-smoke-tests.md). It creates the instance if it's missing,
// drops and recreates SPANNER_DATABASE (default justabill_e2e, so the dev
// database justabill is never touched), applies db/schema.sql, and inserts the
// db/fixture rows that the Go integration tests seed too.
//
// It drops a database, so it refuses to run unless SPANNER_EMULATOR_HOST is set.
//
//	SPANNER_EMULATOR_HOST=localhost:9010 go run ./cmd/e2e-seed
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"

	"cloud.google.com/go/spanner"
	database "cloud.google.com/go/spanner/admin/database/apiv1"
	databasepb "cloud.google.com/go/spanner/admin/database/apiv1/databasepb"
	instance "cloud.google.com/go/spanner/admin/instance/apiv1"
	instancepb "cloud.google.com/go/spanner/admin/instance/apiv1/instancepb"
	"google.golang.org/api/iterator"

	"github.com/justabill-org/justabill/db/fixture"
	"github.com/justabill-org/justabill/db/schema"
	"github.com/justabill-org/justabill/obs"
)

// config names the database to seed.
type config struct {
	Project  string
	Instance string
	Database string
}

func (c config) projectPath() string  { return "projects/" + c.Project }
func (c config) instancePath() string { return c.projectPath() + "/instances/" + c.Instance }
func (c config) databasePath() string { return c.instancePath() + "/databases/" + c.Database }

func main() {
	os.Exit(obs.Main(context.Background(), obs.Config{Service: "justabill-db", Environment: os.Getenv("APP_ENV")},
		func(ctx context.Context, _ *slog.Logger) error { return run(ctx, os.Getenv, os.Stdout) }))
}

func run(ctx context.Context, getenv func(string) string, stdout io.Writer) error {
	cfg, err := loadConfig(getenv)
	if err != nil {
		return err
	}
	if err = seed(ctx, cfg); err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "Seeded %s with the fixture\n", cfg.databasePath())
	return err
}

// loadConfig reads the target from the environment. It fails without
// SPANNER_EMULATOR_HOST, before any client exists, so the seed can't reach a
// real instance.
func loadConfig(getenv func(string) string) (config, error) {
	if getenv("SPANNER_EMULATOR_HOST") == "" {
		return config{}, errors.New("SPANNER_EMULATOR_HOST is not set; e2e-seed drops a database, " +
			"so it only runs against the Spanner emulator")
	}
	or := func(key, fallback string) string {
		if v := getenv(key); v != "" {
			return v
		}
		return fallback
	}
	return config{
		Project:  or("SPANNER_PROJECT", "justabill-local"),
		Instance: or("SPANNER_INSTANCE", "test-instance"),
		Database: or("SPANNER_DATABASE", "justabill_e2e"),
	}, nil
}

// seed creates the instance if needed, recreates the database from
// db/schema.sql, and applies the fixture.
func seed(ctx context.Context, cfg config) error {
	ddl, err := schema.Read()
	if err != nil {
		return err
	}
	muts, err := fixture.Mutations()
	if err != nil {
		return err
	}
	if err = ensureInstance(ctx, cfg); err != nil {
		return err
	}
	if err = recreateDatabase(ctx, cfg, ddl); err != nil {
		return err
	}

	client, err := spanner.NewClient(ctx, cfg.databasePath())
	if err != nil {
		return fmt.Errorf("spanner client: %w", err)
	}
	defer client.Close()
	if _, err = client.Apply(ctx, muts); err != nil {
		return fmt.Errorf("apply the fixture: %w", err)
	}
	return nil
}

// ensureInstance creates the emulator instance unless it exists. Listing
// instead of reading one keeps "missing" from depending on error codes.
func ensureInstance(ctx context.Context, cfg config) error {
	admin, err := instance.NewInstanceAdminClient(ctx)
	if err != nil {
		return fmt.Errorf("instance admin client: %w", err)
	}
	defer admin.Close()

	it := admin.ListInstances(ctx, &instancepb.ListInstancesRequest{Parent: cfg.projectPath()})
	for {
		inst, nextErr := it.Next()
		if errors.Is(nextErr, iterator.Done) {
			break
		}
		if nextErr != nil {
			return fmt.Errorf("list instances: %w", nextErr)
		}
		if inst.GetName() == cfg.instancePath() {
			return nil
		}
	}

	op, err := admin.CreateInstance(ctx, &instancepb.CreateInstanceRequest{
		Parent:     cfg.projectPath(),
		InstanceId: cfg.Instance,
		Instance: &instancepb.Instance{
			Config:      cfg.projectPath() + "/instanceConfigs/emulator-config",
			DisplayName: cfg.Instance,
			NodeCount:   1,
		},
	})
	if err != nil {
		return fmt.Errorf("create instance %s: %w", cfg.Instance, err)
	}
	if _, err = op.Wait(ctx); err != nil {
		return fmt.Errorf("wait for instance %s: %w", cfg.Instance, err)
	}
	return nil
}

// recreateDatabase drops the database if it exists, then creates it with the
// schema's statements.
func recreateDatabase(ctx context.Context, cfg config, ddl []string) error {
	admin, err := database.NewDatabaseAdminClient(ctx)
	if err != nil {
		return fmt.Errorf("database admin client: %w", err)
	}
	defer admin.Close()

	exists, err := databaseExists(ctx, admin, cfg)
	if err != nil {
		return err
	}
	if exists {
		err = admin.DropDatabase(ctx, &databasepb.DropDatabaseRequest{Database: cfg.databasePath()})
		if err != nil {
			return fmt.Errorf("drop database %s: %w", cfg.Database, err)
		}
	}

	op, err := admin.CreateDatabase(ctx, &databasepb.CreateDatabaseRequest{
		Parent:          cfg.instancePath(),
		CreateStatement: fmt.Sprintf("CREATE DATABASE `%s`", cfg.Database),
		ExtraStatements: ddl,
	})
	if err != nil {
		return fmt.Errorf("create database %s: %w", cfg.Database, err)
	}
	if _, err = op.Wait(ctx); err != nil {
		return fmt.Errorf("wait for database %s: %w", cfg.Database, err)
	}
	return nil
}

func databaseExists(ctx context.Context, admin *database.DatabaseAdminClient, cfg config) (bool, error) {
	it := admin.ListDatabases(ctx, &databasepb.ListDatabasesRequest{Parent: cfg.instancePath()})
	for {
		db, err := it.Next()
		if errors.Is(err, iterator.Done) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("list databases: %w", err)
		}
		if db.GetName() == cfg.databasePath() {
			return true, nil
		}
	}
}
