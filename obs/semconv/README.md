# Telemetry conventions registry

Every custom attribute, metric and event name the API, pipeline and web app emit is defined once
here, under `justabill.*`, in OpenTelemetry's
[semantic-convention format](https://github.com/open-telemetry/weaver/blob/v0.26.1/schemas/semconv-syntax.md).
[Weaver](https://github.com/open-telemetry/weaver) v0.26.1 checks the registry and generates the
constants both languages use, so names can't drift apart. Design: `docs/design/53-observability.md`,
Decision 3 and the signals catalogue.

| Path | What it is |
|---|---|
| `registry/manifest.yaml` | The registry, and the upstream conventions it depends on (pinned) |
| `registry/attributes.yaml`, `metrics.yaml`, `events.yaml` | Our names |
| `policies/justabill.rego` | Our rule on top of Weaver's: every name we define starts with `justabill.` |
| `templates/registry/go`, `templates/registry/ts` | Templates for the two generated files |
| `names.go`, `../../web/src/lib/obs/names.ts` | Generated; never edit them by hand |

## Adding or changing a name

1. Edit the YAML under `registry/`. Every name needs a `brief` and a `stability`, and every metric
   an `instrument` and a [UCUM](https://ucum.org/ucum) `unit` (`s`, `By`, or a `{thing}` count).
   Use an upstream name (`http.route`, `server.address`, `gen_ai.request.model`) with `ref:`
   instead of defining your own; the policy rejects a definition outside `justabill.*`.
2. Run `task semconv`. It runs `weaver registry check`, then regenerates `names.go` and `names.ts`.
   Weaver runs in Docker (`scripts/semconv.sh`) and fetches the pinned upstream registries from
   GitHub, so it needs network access. Expect three warnings about upstream GenAI groups without
   a `requirement_level`; they come from the upstream registry, not ours.
3. Commit the YAML and both generated files together. CI's Web Tests job runs
   `scripts/semconv.sh --check` and fails if the registry breaks a rule or either file is stale.

Generated identifiers drop the `justabill.` prefix: `justabill.job.name` is `semconv.JobNameKey`
in Go and `ATTR_JOB_NAME` in TypeScript; its enum values are `semconv.JobOutcomeFailed` and
`JOB_OUTCOME_VALUE_FAILED`; a metric has `…Name`, `…Unit` and `…Description` constants
(`METRIC_…`, `METRIC_…_UNIT`, `METRIC_…_DESCRIPTION`); an event is `…Event` (`EVENT_…`).
