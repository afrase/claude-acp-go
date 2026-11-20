package claudeacp

import (
	"encoding/json"

	acp "github.com/afrase/acp-go/unstable"

	"github.com/afrase/claude-acp-go/claudeagent"
)

// A background subagent's totals. A foreground subagent's Agent call ends
// with what the subagent used in its toolResponse (totalTokens,
// totalToolUseCount, totalDurationMs). A background one's call ends at
// once, its toolResponse saying only that it launched (status
// "async_launched"), and claude tells what it used only in its task
// notification, as the task ends. That goes to the client as a
// tool_call_update of the Agent call with only _meta: a toolResponse of
// those three fields, named as a foreground subagent's are, so a client
// reads them where it already does.

// totals is what a subagent used, as an Agent call's toolResponse names
// it.
type totals struct {
	TotalTokens       int   `json:"totalTokens"`
	TotalToolUseCount int   `json:"totalToolUseCount"`
	TotalDurationMs   int64 `json:"totalDurationMs"`
}

// launched is an Agent call that started a subagent in the background: its
// id, tool and stream ("" at the top, else a subagent's Agent call).
type launched struct{ call, name, parent string }

// launchedAsync says a call of the tool name, with result b and the
// tool's own output structured, started a subagent in the background:
// its result is the launch, and says nothing of what the subagent used.
func launchedAsync(name string, b claudeagent.Block, structured json.RawMessage) bool {
	if name != "Agent" && name != "Task" || b.IsError {
		return false
	}
	var r struct {
		Status string `json:"status"`
	}
	return json.Unmarshal(structured, &r) == nil && r.Status == "async_launched"
}

// launchedLocked keeps t, whose result is b, if it started a subagent in
// the background, until claude's notification of the subagent's end.
func (ts *tools) launchedLocked(t *tool, b claudeagent.Block, structured json.RawMessage) {
	if launchedAsync(t.name, b, structured) {
		ts.launches[t.id] = launched{call: t.id, name: t.name, parent: t.parent}
	}
}

// subagentEnded is claude's notification of a task's end: a background
// subagent's totals go on the Agent call that started it, which is
// forgotten then.
func (ts *tools) subagentEnded(m *claudeagent.TaskNotification) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	l, ok := ts.launches[m.ToolUseID]
	if !ok {
		return
	}
	delete(ts.launches, m.ToolUseID)
	if u := m.Usage; u != nil {
		ts.totalsLocked(l, totals{TotalTokens: u.TotalTokens, TotalToolUseCount: u.ToolUses, TotalDurationMs: u.DurationMS})
	}
}

// totals tells the client of l's subagent's totals, as totalsLocked.
func (ts *tools) totals(l launched, t totals) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.totalsLocked(l, t)
}

// totalsLocked tells the client what l's subagent used: an update of the
// call that says only _meta.
func (ts *tools) totalsLocked(l launched, t totals) {
	response, _ := json.Marshal(t) // ints only: it can't fail
	meta := claudeCodeMeta{ToolName: l.name, ParentToolUseID: l.parent, ToolResponse: response}.meta()
	ts.out.emit(l.parent, acp.SessionUpdate{SessionUpdate: "tool_call_update", ToolCallUpdate: &acp.ToolCallUpdate{
		ToolCallID: acp.ToolCallID(l.call), Meta: meta,
	}})
}

// A load replays a background subagent's totals where the history has
// them: the <usage> of claude's <task-notification> of its end, which
// names the call that started it (<tool-use-id>).

// launched keeps t, a replayed call whose result is b, if it started a
// subagent in the background. A subagent's transcript keeps no tool's
// own output, so a call there launched one in the background when the
// subagent it started was (its input or its sidecar says).
func (r *replayer) launched(t *tool, b claudeagent.Block, structured json.RawMessage) {
	kid := r.launchOf(t.id)
	unsaid := len(structured) == 0 || string(structured) == "null"
	if launchedAsync(t.name, b, structured) || unsaid && kid != nil && kid.background && !b.IsError {
		r.launches[t.id] = launched{call: t.id, name: t.name, parent: t.parent}
	}
}

// subagentEnded replays the totals of the background subagent whose end
// e is, once.
func (r *replayer) subagentEnded(e taskNote) {
	l, ok := r.launches[e.call]
	if !ok || e.usage == nil {
		return
	}
	delete(r.launches, e.call)
	r.s.tools.totals(l, *e.usage)
}
