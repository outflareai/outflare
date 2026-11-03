package customers

import (
	"context"

	"github.com/outflareai/outflare/engine/internal/jobs"
)

// JobMetricEvent is the job that adds an event to the live metrics that
// count it (engine/internal/recmetrics).
const JobMetricEvent = "metric.event"

// MetricEventAdded hands an event to the live metrics when its brand has
// metrics on and one of them counts the event: by name, or as usage (an
// event the product sent through ingest). Call it in the transaction that
// stored the event.
func MetricEventAdded(ctx context.Context, q Querier, ws, brand, eventID, name string, usage bool) error {
	var hit bool
	if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM metric_definitions d JOIN metric_settings s ON s.brand_id = d.brand_id AND s.enabled_at IS NOT NULL
		WHERE d.brand_id = $1 AND d.workspace_id = $2 AND d.status = 'active' AND ($3 = ANY (d.events) OR (d.usage AND $4)))`,
		brand, ws, name, usage).Scan(&hit); err != nil || !hit {
		return err
	}
	_, err := jobs.Enqueue(ctx, q, jobs.NewJob{Kind: JobMetricEvent, WorkspaceID: &ws, Payload: map[string]string{"event_id": eventID},
		IdempotencyKey: "metric-event:" + eventID})
	return err
}
