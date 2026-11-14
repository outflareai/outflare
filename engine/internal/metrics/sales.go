package metrics

import "github.com/prometheus/client_golang/prometheus"

var (
	salesSendResults = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "sales", Name: "send_results_total",
		Help: "Committed Sales send results, by status: accepted, failed or uncertain. Uncertain means reconciliation is required, never safe to resend.",
	}, []string{"status"})
	salesStoredMessages = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "sales", Name: "stored_messages_total",
		Help: "New Sales messages retained by mailbox synchronization, by direction. Repeated provider copies do not count again.",
	}, []string{"direction"})
	salesAttention    = desc("sales", "messages", "Sales sends requiring attention, by status: queued, sending, failed or uncertain. Counts are global, without workspace or person labels.", "status")
	salesAttentionAge = desc("sales", "oldest_message_seconds", "Age of the oldest Sales send requiring attention, by status. Queued and sending should stay near zero; uncertain needs provider reconciliation.", "status")
)

// SalesSendResult records a committed result, never message contents or ids.
func SalesSendResult(status string) {
	switch status {
	case "accepted", "failed", "uncertain":
		salesSendResults.WithLabelValues(status).Inc()
	}
}

// SalesMessageStored counts only newly retained messages after commit.
func SalesMessageStored(direction string) {
	switch direction {
	case "incoming", "outgoing":
		salesStoredMessages.WithLabelValues(direction).Inc()
	}
}
