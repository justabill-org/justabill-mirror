package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"os"
	"strings"
	"testing"

	"cloud.google.com/go/spanner"
	instance "cloud.google.com/go/spanner/admin/instance/apiv1"
	instancepb "cloud.google.com/go/spanner/admin/instance/apiv1/instancepb"

	"github.com/justabill-org/justabill/db/fixture"
)

func env(vars map[string]string) func(string) string {
	return func(key string) string { return vars[key] }
}

func TestLoadConfig(t *testing.T) {
	cfg, err := loadConfig(env(map[string]string{"SPANNER_EMULATOR_HOST": "localhost:9010"}))
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	want := config{Project: "justabill-local", Instance: "test-instance", Database: "justabill_e2e"}
	if cfg != want {
		t.Errorf("defaults = %+v, want %+v", cfg, want)
	}

	cfg, err = loadConfig(env(map[string]string{
		"SPANNER_EMULATOR_HOST": "localhost:9010",
		"SPANNER_PROJECT":       "p",
		"SPANNER_INSTANCE":      "i",
		"SPANNER_DATABASE":      "d",
	}))
	if err != nil || cfg.databasePath() != "projects/p/instances/i/databases/d" {
		t.Errorf("overrides: %s, %v; want projects/p/instances/i/databases/d", cfg.databasePath(), err)
	}
}

// TestRunRefusesWithoutEmulator: without SPANNER_EMULATOR_HOST the seed fails
// before it creates any client, so it can't drop a real database.
func TestRunRefusesWithoutEmulator(t *testing.T) {
	err := run(context.Background(), env(map[string]string{"SPANNER_PROJECT": "prod"}), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "SPANNER_EMULATOR_HOST") {
		t.Fatalf("run() = %v, want an error naming SPANNER_EMULATOR_HOST", err)
	}
}

// TestSeedTwice runs the seed against an instance that doesn't exist yet, then
// again over its own database, and reads HR 1 back.
func TestSeedTwice(t *testing.T) {
	if os.Getenv("SPANNER_EMULATOR_HOST") == "" {
		t.Skip("SPANNER_EMULATOR_HOST not set; skipping emulator test")
	}
	ctx := context.Background()
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	suffix := hex.EncodeToString(b)
	vars := map[string]string{
		"SPANNER_EMULATOR_HOST": os.Getenv("SPANNER_EMULATOR_HOST"),
		"SPANNER_PROJECT":       os.Getenv("SPANNER_PROJECT"),
		"SPANNER_INSTANCE":      "e2e-seed-" + suffix,
	}
	cfg, err := loadConfig(env(vars))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { deleteInstance(t, cfg) })

	for i := range 2 {
		var out strings.Builder
		if err = run(ctx, env(vars), &out); err != nil {
			t.Fatalf("run %d: %v", i+1, err)
		}
		if !strings.Contains(out.String(), cfg.databasePath()) {
			t.Errorf("run %d printed %q, want the database path", i+1, out.String())
		}
	}

	client, err := spanner.NewClient(ctx, cfg.databasePath())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	row, err := client.Single().ReadRow(ctx, "bills", spanner.Key{fixture.HouseBill}, []string{"title", "congress"})
	if err != nil {
		t.Fatalf("read %s: %v", fixture.HouseBill, err)
	}
	var title string
	var congress int64
	if err = row.Columns(&title, &congress); err != nil {
		t.Fatal(err)
	}
	if title != fixture.Title || congress != fixture.Congress {
		t.Errorf("%s = %q (%d), want %q (%d)", fixture.HouseBill, title, congress, fixture.Title, fixture.Congress)
	}
}

func deleteInstance(t *testing.T, cfg config) {
	t.Helper()
	ctx := context.Background()
	admin, err := instance.NewInstanceAdminClient(ctx)
	if err != nil {
		t.Logf("instance admin client: %v", err)
		return
	}
	defer admin.Close()
	if err = admin.DeleteInstance(ctx, &instancepb.DeleteInstanceRequest{Name: cfg.instancePath()}); err != nil {
		t.Logf("delete instance %s: %v", cfg.instancePath(), err)
	}
}
