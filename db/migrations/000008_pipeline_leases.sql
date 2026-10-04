-- pipeline_leases holds the single-runner lease (docs/design/80-pipeline-operability.md,
-- Decision 1A): one process at a time, a serve replica or a backfill, runs the sync jobs.
-- holder is the pod name plus 8 random hex characters per process. expires_at is set from
-- Spanner's clock; a released lease keeps its row with expires_at at the release time, so the
-- last holder stays visible.
CREATE TABLE pipeline_leases (
  name STRING(MAX) NOT NULL,
  holder STRING(MAX) NOT NULL,
  acquired_at TIMESTAMP NOT NULL,
  expires_at TIMESTAMP NOT NULL,
) PRIMARY KEY (name);
