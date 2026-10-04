package spannerdb

import (
	"context"
	"fmt"

	"cloud.google.com/go/spanner"
	"cloud.google.com/go/spanner/apiv1/spannerpb"
)

// Client wraps a Spanner client for database operations.
type Client struct {
	Spanner *spanner.Client
}

// Option configures the Spanner client [NewClient] creates.
type Option func(*spanner.ClientConfig)

// WithDatabaseRole makes every session the client creates assume the
// fine-grained access control database role, so Spanner checks that role's
// grants instead of the caller's database-level IAM roles
// (docs/design/609-api-database-role.md). An empty role sets none. The
// emulator accepts a role but doesn't enforce it.
func WithDatabaseRole(role string) Option {
	return func(c *spanner.ClientConfig) { c.DatabaseRole = role }
}

// WithBatchPriority runs every SQL query and DML statement the client sends at
// [spannerpb.RequestOptions_PRIORITY_LOW], so a batch job's long reads yield
// the instance's CPU to the API's user reads instead of competing with them
// (#867). The pipeline's commands use it; the API doesn't. Point reads
// (ReadRow) and commits keep the default priority, and a statement given its
// own priority keeps it: the lease's statements run at high priority, so a
// busy instance can't starve the lease and stop every job.
func WithBatchPriority() Option {
	return func(c *spanner.ClientConfig) { c.QueryOptions.Priority = spannerpb.RequestOptions_PRIORITY_LOW }
}

// NewClient creates a new Spanner client. The client traces through the global
// OpenTelemetry tracer provider, and NewClient turns on its OpenTelemetry
// metrics (sessions, GFE latency) for the global meter provider, so a service
// calls obs.Start first. Without it both globals are no-ops.
func NewClient(ctx context.Context, project, instance, database string, opts ...Option) (*Client, error) {
	// A process-wide switch in the Spanner library; each client reads it when it's created.
	spanner.EnableOpenTelemetryMetrics()
	db := fmt.Sprintf("projects/%s/instances/%s/databases/%s", project, instance, database)
	sc, err := spanner.NewClientWithConfig(ctx, db, clientConfig(opts...))
	if err != nil {
		return nil, fmt.Errorf("creating spanner client: %w", err)
	}
	return &Client{Spanner: sc}, nil
}

// clientConfig is the zero configuration with opts applied. The library fills
// in the same defaults [spanner.NewClient] passes (its session pool settings
// are gone: every client uses multiplexed sessions).
func clientConfig(opts ...Option) spanner.ClientConfig {
	var c spanner.ClientConfig
	for _, o := range opts {
		o(&c)
	}
	return c
}

// Close closes the Spanner client.
func (c *Client) Close() { c.Spanner.Close() }

// Ping verifies the Spanner connection by running a trivial query.
func (c *Client) Ping(ctx context.Context) error {
	iter := c.Spanner.Single().Query(ctx, spanner.NewStatement("SELECT 1"))
	defer iter.Stop()
	_, err := iter.Next()
	return err
}

// NewBillRepo creates a BillRepo backed by Spanner.
func NewBillRepo(c *Client) *BillRepository { return &BillRepository{client: c.Spanner} }

// NewMemberRepo creates a MemberRepo backed by Spanner.
func NewMemberRepo(c *Client) *MemberRepository { return &MemberRepository{client: c.Spanner} }

// NewUserRepo creates a UserRepo backed by Spanner.
func NewUserRepo(c *Client) *UserRepository { return &UserRepository{client: c.Spanner} }

// NewVoteRepo creates a VoteRepo backed by Spanner.
func NewVoteRepo(c *Client) *VoteRepository { return &VoteRepository{client: c.Spanner} }

// NewCongressRepo creates a CongressRepo backed by Spanner.
func NewCongressRepo(c *Client) *CongressRepository { return &CongressRepository{client: c.Spanner} }

// NewScorecard creates a Scorecard backed by Spanner.
func NewScorecard(c *Client) *ScorecardService { return &ScorecardService{client: c.Spanner} }

// NewGraphRepo creates a GraphRepo backed by the civic_graph property graph.
func NewGraphRepo(c *Client) *GraphRepository { return &GraphRepository{client: c.Spanner} }

// NewLawRepo creates a LawRepo backed by Spanner.
func NewLawRepo(c *Client) *LawRepository { return &LawRepository{client: c.Spanner} }

// NewAggregateReader creates an AggregateReader backed by Spanner.
func NewAggregateReader(c *Client) *AggregateRepository {
	return &AggregateRepository{client: c.Spanner}
}

// NewPipelineStore creates a PipelineStore backed by Spanner.
func NewPipelineStore(c *Client) *PipelineStoreImpl { return &PipelineStoreImpl{client: c.Spanner} }
