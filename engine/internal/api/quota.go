package api

import (
	"net/http"

	"github.com/outflareai/outflare/engine/internal/quota"
)

// allowQuota asks the quota checker (Options.Quota) whether the workspace
// may add n of resource. When it refuses, it writes 402 quota_exceeded with
// the checker's message and returns false. A server without a checker
// allows everything.
func (s *Server) allowQuota(w http.ResponseWriter, r *http.Request, workspaceID, resource string, n int64) bool {
	err := quota.Check(r.Context(), s.quota, s.log, workspaceID, resource, n)
	if qe, ok := quota.Refusal(err); ok {
		writeError(w, r, http.StatusPaymentRequired, "quota_exceeded", qe.Message)
		return false
	}
	return true
}
