package claudeacp

import (
	"bytes"
	"context"
	"encoding/json"

	acp "github.com/afrase/acp-go/unstable"
)

// ContextUsageMethod is an extension of this agent's: how an open
// session's context window is spent, as claude's get_context_usage control
// request tells it. It asks the model nothing, forks nothing and writes
// nothing to the transcript, and it can be asked while a turn runs. It
// isn't part of ACP.
//
// The answer is claude's, whole, as claude sent it: a JSON object. The
// part of it claudeagent.ContextUsage decodes is categories (each a name,
// tokens, a kind of "used", "free", "buffer" or "deferred", and
// isDeferred), totalTokens, maxTokens, rawMaxTokens, percentage and model.
// Everything else (the lists a "full" answer carries, say) is claude's
// own, undocumented: claude may add, rename or reword any of it, and a
// category's name is English for display, never to classify on (kind is).
const ContextUsageMethod = "_session/contextUsage"

// ContextUsageRequest is ContextUsageMethod's params: the open session,
// and claude's own detail, "summary" or "full"; "" leaves it out, for
// claude's default.
type ContextUsageRequest struct {
	SessionID string `json:"sessionId"`
	Detail    string `json:"detail,omitempty"`
}

// contextUsage answers ContextUsageMethod with claude's answer, passed
// through: nothing of it is retyped, so a field claude adds reaches the
// client and one it changes can't fail the call.
func (a *agent) contextUsage(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
	var req ContextUsageRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, invalidParams("%s: %v", ContextUsageMethod, err)
	}
	s, err := a.session(acp.SessionID(req.SessionID))
	if err != nil {
		return nil, err
	}
	ask := struct {
		Detail string `json:"detail,omitempty"`
	}{req.Detail}
	var out json.RawMessage
	if err := s.cs.Request(ctx, "get_context_usage", ask, &out); err != nil {
		return nil, err
	}
	if t := bytes.TrimSpace(out); len(t) == 0 || bytes.Equal(t, []byte("null")) {
		return json.RawMessage(`{}`), nil
	}
	return out, nil
}
