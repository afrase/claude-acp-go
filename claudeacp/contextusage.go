package claudeacp

import (
	"bytes"
	"context"
	"encoding/json"

	acp "github.com/afrase/acp-go/unstable"
)

// ContextUsageMethod is an extension of this agent's: how an open
// session's context window is spent, as claude's get_context_usage control
// request tells it. No turn runs, nothing is forked and nothing is
// written to the transcript, and it can be asked while a turn runs
// (claude answered in milliseconds while a command of a turn ran, in a
// recording of Claude Code 2.1.331). To answer "full", claude may call
// the API's token-counting endpoint (it did, once a session); "summary"
// is its own estimate. It isn't part of ACP.
//
// The answer is claude's, whole, as claude sent it: a JSON object. The
// part of it claudeagent.ContextUsage decodes is categories (each an id,
// a name, tokens, a kind of "used", "free", "buffer" or "deferred", and
// isDeferred), totalTokens, maxTokens, rawMaxTokens, percentage and model.
// Everything else is claude's own, undocumented: claude may add, rename
// or reword any of it. In the recording, both details carried the lists
// memoryFiles (path, type, tokens), agents (agentType, source, path,
// tokens), mcpTools (name, serverName, tokens, isLoaded) and skills
// (counts, and skillFrontmatter: name, source, tokens), and more.
// A category's name is English for display: classify on kind and tell
// one from another by id ("messages" is the conversation's, which a
// "summary" answer doesn't have).
const ContextUsageMethod = "_session/contextUsage"

// ContextUsageRequest is ContextUsageMethod's params: the open session,
// and claude's own detail, "summary" or "full"; "" leaves it out, for
// claude's default, which answers as "full" does.
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
