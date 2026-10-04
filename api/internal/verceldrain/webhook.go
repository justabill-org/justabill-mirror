package verceldrain

import (
	"cmp"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/log"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"

	"github.com/justabill-org/justabill/obs"
	jsemconv "github.com/justabill-org/justabill/obs/semconv"
)

// Webhook event types (https://vercel.com/docs/webhooks/webhooks-api).
const (
	alertsTriggered  = "alerts.triggered"
	deploymentPrefix = "deployment."
	// defaultTarget is a deployment's environment when Vercel's target is null.
	defaultTarget = "preview"
	unknown       = "unknown"
)

// deploymentState returns the state name a deployment event becomes
// (justabill.vercel.deployment.state), and false for events that aren't forwarded: integration and
// check events aren't deployments changing state.
func deploymentState(kind string) (string, bool) {
	switch kind {
	case "created", "succeeded", "ready", "promoted", "rollback", "canceled", "error":
		return strings.ToUpper(kind), true
	default:
		return "", false
	}
}

// webhookEvent is a team webhook delivery.
type webhookEvent struct {
	Type      string          `json:"type"`
	CreatedAt json.RawMessage `json:"createdAt"`
	Payload   json.RawMessage `json:"payload"`
}

// alertPayload is the allowlisted part of an alerts.triggered payload.
type alertPayload struct {
	Severity string `json:"severity"`
	Links    struct {
		Observability string `json:"observability"`
	} `json:"links"`
	Alerts []struct {
		Type   string `json:"type"`
		Title  string `json:"title"`
		Metric string `json:"metric"`
	} `json:"alerts"`
}

// deploymentPayload is the allowlisted part of a deployment.* payload.
type deploymentPayload struct {
	Target     string `json:"target"`
	Deployment struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"deployment"`
	Links struct {
		Deployment string `json:"deployment"`
	} `json:"links"`
	// ToDeploymentID is set on deployment.rollback, which has no deployment object.
	ToDeploymentID string `json:"toDeploymentId"`
}

// webhook turns an anomaly alert into justabill.vercel.alert events and a deployment change into
// a justabill.vercel.deployment event. Other event types get 200 and are dropped, so Vercel
// doesn't retry them.
func (rc *Receiver) webhook(w http.ResponseWriter, r *http.Request, body []byte) {
	ctx := r.Context()

	var event webhookEvent
	if err := json.Unmarshal(body, &event); err != nil {
		rc.logger.WarnContext(ctx, "vercel-drain: unparseable webhook")
		http.Error(w, "unparseable webhook", http.StatusBadRequest)

		return
	}

	at := eventTime(event.CreatedAt)

	var err error

	switch {
	case event.Type == alertsTriggered:
		err = rc.alert(ctx, at, event.Payload)
	case strings.HasPrefix(event.Type, deploymentPrefix):
		err = rc.deployment(ctx, at, strings.TrimPrefix(event.Type, deploymentPrefix), event.Payload)
	default:
		rc.logger.DebugContext(ctx, "vercel-drain: ignored webhook", "type", event.Type)
	}

	if err != nil {
		rc.logger.WarnContext(ctx, "vercel-drain: unparseable webhook payload", "type", event.Type)
		http.Error(w, "unparseable payload", http.StatusBadRequest)

		return
	}

	w.WriteHeader(http.StatusOK)
}

// alert emits one justabill.vercel.alert event per alert in the payload.
func (rc *Receiver) alert(ctx context.Context, at time.Time, raw json.RawMessage) error {
	var p alertPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}

	sev := alertSeverity(p.Severity)
	if len(p.Alerts) == 0 {
		rc.event(ctx, at, jsemconv.VercelAlertEvent, sev,
			join("Vercel alert", p.Links.Observability), jsemconv.VercelAlertTypeKey.String(unknown))

		return nil
	}

	for _, a := range p.Alerts {
		body := join(cmp.Or(a.Title, "Vercel alert"), a.Metric, p.Links.Observability)
		rc.event(ctx, at, jsemconv.VercelAlertEvent, sev, body,
			jsemconv.VercelAlertTypeKey.String(cmp.Or(a.Type, unknown)))
	}

	return nil
}

// deployment emits a justabill.vercel.deployment event for the deployment states we track.
func (rc *Receiver) deployment(ctx context.Context, at time.Time, kind string, raw json.RawMessage) error {
	state, ok := deploymentState(kind)
	if !ok {
		rc.logger.DebugContext(ctx, "vercel-drain: ignored webhook", "type", deploymentPrefix+kind)

		return nil
	}

	var p deploymentPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}

	attrs := []attribute.KeyValue{
		jsemconv.VercelDeploymentStateKey.String(state),
		semconv.DeploymentEnvironmentNameKey.String(cmp.Or(p.Target, defaultTarget)),
	}
	if id := cmp.Or(p.Deployment.ID, p.ToDeploymentID); id != "" {
		attrs = append(attrs, semconv.DeploymentID(id))
	}

	sev := log.SeverityInfo

	switch state {
	case "ERROR":
		sev = log.SeverityError
	case "CANCELED":
		sev = log.SeverityWarn
	}

	body := join("Vercel deployment "+state, p.Deployment.Name, p.Links.Deployment)
	rc.event(ctx, at, jsemconv.VercelDeploymentEvent, sev, body, attrs...)

	return nil
}

// event emits one event record.
func (rc *Receiver) event(
	ctx context.Context, at time.Time, name string, sev log.Severity, body string, attrs ...attribute.KeyValue,
) {
	var rec log.Record

	rec.SetEventName(name)
	rec.SetTimestamp(at)
	rec.SetObservedTimestamp(time.Now())
	rec.SetSeverity(sev)
	rec.SetBody(attribute.StringValue(obs.Redact(truncate(body, maxMessageBytes))))
	rec.AddAttributes(attrs...)

	rc.records.Emit(ctx, rec)
}

// alertSeverity maps Vercel's alert severity (low, medium, high, critical) to OpenTelemetry's.
func alertSeverity(s string) log.Severity {
	switch s {
	case "high", "critical":
		return log.SeverityError
	default:
		return log.SeverityWarn
	}
}

// eventTime reads a webhook's createdAt, which Vercel documents as a JavaScript timestamp
// (milliseconds) but may send as an ISO 8601 string. Anything else is the time it arrived.
func eventTime(raw json.RawMessage) time.Time {
	var ms int64
	if err := json.Unmarshal(raw, &ms); err == nil && ms > 0 {
		return time.UnixMilli(ms)
	}

	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if t, parseErr := time.Parse(time.RFC3339, s); parseErr == nil {
			return t
		}
	}

	return time.Now()
}

// join joins the non-empty parts with " · ".
func join(parts ...string) string {
	kept := parts[:0:0]
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}

	return strings.Join(kept, " · ")
}
