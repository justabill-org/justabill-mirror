package health_test

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"testing"

	instance "cloud.google.com/go/spanner/admin/instance/apiv1"
	"cloud.google.com/go/spanner/admin/instance/apiv1/instancepb"

	"github.com/justabill-org/justabill/api/internal/health"
	"github.com/justabill-org/justabill/db/spannerdb"
	"github.com/justabill-org/justabill/db/testdb"
)

// TestReadyzOnEmulator checks /readyz's SELECT 1 against a real Spanner: the emulator, in CI's
// API Tests job and locally. It skips without SPANNER_EMULATOR_HOST.
func TestReadyzOnEmulator(t *testing.T) {
	if os.Getenv("SPANNER_EMULATOR_HOST") == "" {
		t.Skip("SPANNER_EMULATOR_HOST not set; skipping emulator test")
	}
	ensureInstance(t)
	client, _ := testdb.NewEmpty(t)

	h := health.New(slog.New(slog.DiscardHandler), &spannerdb.Client{Spanner: client}).Wrap(notFound)
	if code, body, _ := get(t, h, "/readyz"); code != http.StatusOK || body.Checks["spanner"] != "ok" {
		t.Errorf("/readyz on the emulator = %d %+v, want 200 with spanner ok", code, body)
	}
}

// ensureInstance creates testdb's emulator instance if it doesn't exist yet. The DB Tests job
// creates it; the API Tests job's emulator starts empty.
func ensureInstance(t *testing.T) {
	t.Helper()
	project := envOr("SPANNER_PROJECT", "test-project")
	name := envOr("SPANNER_INSTANCE", "test-instance")

	admin, err := instance.NewInstanceAdminClient(t.Context())
	if err != nil {
		t.Fatalf("instance admin client: %v", err)
	}
	t.Cleanup(func() { _ = admin.Close() })

	path := fmt.Sprintf("projects/%s/instances/%s", project, name)
	if _, err = admin.GetInstance(t.Context(), &instancepb.GetInstanceRequest{Name: path}); err == nil {
		return
	}
	op, err := admin.CreateInstance(t.Context(), &instancepb.CreateInstanceRequest{
		Parent:     "projects/" + project,
		InstanceId: name,
		Instance: &instancepb.Instance{
			Config:      fmt.Sprintf("projects/%s/instanceConfigs/emulator-config", project),
			DisplayName: "API tests",
			NodeCount:   1,
		},
	})
	if err != nil {
		t.Fatalf("create instance: %v", err)
	}
	if _, err = op.Wait(t.Context()); err != nil {
		t.Fatalf("wait create instance: %v", err)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
