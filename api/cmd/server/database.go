package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/spf13/viper"

	"github.com/justabill-org/justabill/db/spannerdb"
)

// maxDatabaseRoleLength is the longest database role name Spanner accepts.
const maxDatabaseRoleLength = 128

// openSpanner connects to the database with the options [spannerOptions]
// reads.
func openSpanner(ctx context.Context, project, instance, database string,
	logger *slog.Logger) (*spannerdb.Client, error) {
	opts, err := spannerOptions(ctx, logger)
	if err != nil {
		return nil, err
	}
	return spannerdb.NewClient(ctx, project, instance, database, opts...)
}

// spannerOptions reads SPANNER_DATABASE_ROLE, the fine-grained access control
// role the API's sessions assume (docs/design/609-api-database-role.md), and
// logs it. Production sets it to api, whose grants allow writes only to the
// user tables. Unset, the client sets no role and the service account's
// database-level IAM roles apply. The emulator accepts a role but doesn't
// enforce it.
func spannerOptions(ctx context.Context, logger *slog.Logger) ([]spannerdb.Option, error) {
	role := strings.TrimSpace(viper.GetString("spanner_database_role"))
	if role == "" {
		logger.InfoContext(ctx, "spanner database role: none, the service account's IAM roles apply")
		return nil, nil
	}
	if !validDatabaseRole(role) {
		return nil, fmt.Errorf("SPANNER_DATABASE_ROLE=%q: want a role name such as api "+
			"(a letter, then letters, digits or underscores, at most %d)", role, maxDatabaseRoleLength)
	}
	logger.InfoContext(ctx, "spanner database role", "role", role)
	return []spannerdb.Option{spannerdb.WithDatabaseRole(role)}, nil
}

// validDatabaseRole reports whether role is a name Spanner accepts for a
// database role: a letter, then letters, digits or underscores.
func validDatabaseRole(role string) bool {
	if len(role) > maxDatabaseRoleLength {
		return false
	}
	for i, r := range role {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		case i > 0 && (r >= '0' && r <= '9' || r == '_'):
		default:
			return false
		}
	}
	return role != ""
}
