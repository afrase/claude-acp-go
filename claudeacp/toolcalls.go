package claudeacp

import (
	"cmp"
	"encoding/json"
	"maps"
	"slices"

	acp "github.com/afrase/acp-go/unstable"

	"github.com/afrase/claude-acp-go/claudeagent"
)

// A tool call's life: its start as the model writes it, its input as it
// streams and once whole, claude's word on it as it runs, and its end.

// newMessage forgets the blocks a stream's last message was writing.
func (ts *tools) newMessage(stream string) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	delete(ts.streaming, stream)
}

// start is the model starting a tool use: the call, with no input yet.
func (ts *tools) start(stream string, index int, b *claudeagent.Block) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	t := ts.byID[b.ID]
	if t == nil {
		t = &tool{id: b.ID, name: b.Name, parent: stream}
		ts.byID[t.id] = t
	}
	t.parent = cmp.Or(t.parent, stream) // a request may have shown it before its stream
	if ts.streaming[stream] == nil {
		ts.streaming[stream] = map[int]*tool{}
	}
	ts.streaming[stream][index] = t
	if t.shown {
		return // a permission request showed it whole already
	}
	v := toolView(t.name, nil, ts.cwd)
	ts.sendLocked(t, report{title: v.title, kind: v.kind, rawInput: json.RawMessage(`{}`)})
}

// delta is more of a streaming tool use's input: each field it completes
// is sent, without content, which waits for the whole input.
func (ts *tools) delta(stream string, index int, partial string) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	t := ts.streaming[stream][index]
	if t == nil || t.whole {
		return
	}
	t.partial.WriteString(partial)
	in, n := completeFields(t.partial.String())
	if n <= t.fields {
		return
	}
	t.fields = n
	v := toolView(t.name, in, ts.cwd)
	ts.sendLocked(t, report{title: v.title, kind: v.kind, locations: v.locations, rawInput: in})
}

// input is a tool use as claude's message has it, its input whole.
func (ts *tools) input(stream string, b claudeagent.Block) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.wholeLocked(stream, b.ID, b.Name, b.Input)
}

// wholeLocked is the call id with its whole input, shown first if it
// hasn't been.
func (ts *tools) wholeLocked(stream, id, name string, input json.RawMessage) *tool {
	t := ts.byID[id]
	if t == nil {
		t = &tool{id: id, name: name, parent: stream}
		ts.byID[id] = t
	}
	t.parent = cmp.Or(t.parent, stream)
	t.whole = true
	ts.sendLocked(t, wholeReport(t.name, input, ts.cwd))
	if r, ok := ts.early[id]; ok {
		delete(ts.early, id)
		ts.sendLocked(t, r)
	}
	return t
}

// wholeReport is a call of the tool name shown with its whole input (an
// empty one is {}), live or replayed, so a reopened session shows it as
// the live one did.
func wholeReport(name string, input json.RawMessage, cwd string) report {
	if len(input) == 0 {
		input = json.RawMessage(`{}`)
	}
	v := toolView(name, input, cwd)
	content := v.content
	if content == nil {
		content = []acp.ToolCallContent{}
	}
	return report{title: v.title, kind: v.kind, content: content, locations: v.locations, rawInput: input}
}

// resultReport is a call of the tool name ended by its result b,
// completed or failed, live or replayed; structured is the tool's own
// output (claude's tool_use_result), when claude gave it.
func resultReport(name string, b claudeagent.Block, structured json.RawMessage) report {
	r := report{status: acp.ToolCallStatusCompleted, rawOutput: b.Content}
	if b.IsError {
		r.status = acp.ToolCallStatusFailed
	}
	if name == "ExitPlanMode" {
		r.rawOutput = unfenced(b.Content)
		if !b.IsError {
			r.title = "Exited Plan Mode" // the plan stays as the call's content
		}
	}
	if content, ok := resultContent(name, b, structured); ok {
		r.content = content
	}
	return r
}

// callInput is the input of the call id as the client was last shown it,
// and the stream it is of.
func (ts *tools) callInput(id string) (in launch, stream string) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if t := ts.byID[id]; t != nil {
		_ = json.Unmarshal(t.last.rawInput, &in)
		return in, t.parent
	}
	return in, ""
}

// result is a tool's result: the call ends, completed or failed.
// structured is the tool's own output, when claude gave it, and why says
// why the result isn't, when claude said.
func (ts *tools) result(b claudeagent.Block, structured json.RawMessage, why nonExecution) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	t := ts.byID[b.ToolUseID]
	switch {
	case t == nil:
		return
	case t.denied: // its end is shown: the result adds claude's words, raw
		t.denied = false
		ts.sendLocked(t, report{rawOutput: b.Content, why: why})
		return
	case t.ended:
		return
	}
	t.ended = true
	r := resultReport(t.name, b, structured)
	r.why = why
	ts.sendLocked(t, r)
	ts.out.release(t.id) // its work, if it started a subagent claude never named
}

// progress is a tool still running: each beat goes, saying how long it
// has run (and an Agent call's subagent's type and retries), as
// claude-agent-acp sends them. A beat under an id of its own (claude's
// heartbeats, a subagent's retries) is its parent call's.
func (ts *tools) progress(m *claudeagent.ToolProgress) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	t := ts.byID[m.ToolUseID]
	if t == nil {
		t = ts.byID[m.ParentToolUseID]
	}
	if t == nil || t.ended {
		return
	}
	retry := m.SubagentRetry
	if string(retry) == "null" {
		retry = nil
	}
	beat, err := json.Marshal(struct {
		ElapsedTimeSeconds float64         `json:"elapsedTimeSeconds"`
		SubagentType       string          `json:"subagentType,omitempty"`
		SubagentRetry      json.RawMessage `json:"subagentRetry,omitempty"`
	}{m.ElapsedTimeSeconds, m.SubagentType, retry})
	if err != nil {
		beat = nil // retries that aren't JSON: the beat says it runs only
	}
	ts.sendLocked(t, report{status: acp.ToolCallStatusInProgress, response: beat})
}

// denied is a tool use claude refused without asking.
func (ts *tools) denied(m *claudeagent.PermissionDenied) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	t := ts.byID[m.ToolUseID]
	if t == nil || t.ended {
		return
	}
	t.ended, t.denied = true, true
	// The rule's reason says more than claude's message, which only says
	// it was denied.
	reason := cmp.Or(m.DecisionReason, m.Message)
	response, _ := json.Marshal(struct {
		DecisionReasonType string `json:"decisionReasonType,omitempty"`
		DecisionReason     string `json:"decisionReason,omitempty"`
		Message            string `json:"message,omitempty"`
	}{m.DecisionReasonType, m.DecisionReason, m.Message})
	ts.sendLocked(t, report{status: acp.ToolCallStatusFailed, content: texts("Permission denied: " + reason), response: response})
}

// nonExecution is why a tool's result is claude's words, not the tool's
// output, as claude-agent-acp says it under _meta.claudeCode: the call
// never ran ("user-rejected", "permission-rule", "automode-…") or was
// stopped ("interrupted", "cancelled"); and what the user said, refusing
// it.
type nonExecution struct {
	Kind     string `json:"nonExecutionKind,omitempty"`
	Feedback string `json:"userFeedback,omitempty"`
}

// nonExecutions reads a user message's tool_result_meta: why each
// result it names isn't the tool's output, by the result's call id. An
// entry that doesn't say is left out.
func nonExecutions(raw json.RawMessage) map[string]nonExecution {
	var entries []json.RawMessage
	if json.Unmarshal(raw, &entries) != nil {
		return nil
	}
	why := map[string]nonExecution{}
	for _, e := range entries {
		var m struct {
			ID       string          `json:"id"`
			Kind     string          `json:"non_execution_kind"`
			Feedback json.RawMessage `json:"user_feedback"`
		}
		if json.Unmarshal(e, &m) != nil || m.ID == "" || m.Kind == "" {
			continue
		}
		n := nonExecution{Kind: m.Kind}
		_ = json.Unmarshal(m.Feedback, &n.Feedback) // only text is what the user said
		why[m.ID] = n
	}
	return why
}

// callEnd is why the calls still open end.
type callEnd int

const (
	endedTurn callEnd = iota
	endedCancel
	endedExit
)

// exitedText is what a call claude exited under says.
const exitedText = "Claude exited before this call had a result."

// end ends the top-level calls still open as claude's turn ends;
// incomplete says one of them claude ran (its input came whole) yet gave
// no result. Subagents' calls go on, as a subagent may outlive the turn,
// and so does a compaction still running; one that has ended is
// forgotten. When claude exits nothing can end a call later, so every
// one ends, the compaction's too.
func (ts *tools) end(why callEnd) (incomplete bool) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	exited := why == endedExit
	for _, id := range slices.Sorted(maps.Keys(ts.byID)) {
		t := ts.byID[id]
		if t.ended {
			delete(ts.byID, id) // its hook, if any, has come
			continue
		}
		if !exited && (t.parent != "" || t.agent != "") {
			continue
		}
		t.ended = true
		delete(ts.byID, id)
		var text string
		switch {
		case why == endedCancel:
			text = "The turn was cancelled before this call had a result."
		case exited:
			text = exitedText
		case !t.whole:
			text = "The model stopped before finishing this call; it never ran."
		default:
			text = "The turn ended with no result for this call."
			incomplete = true
		}
		ts.sendLocked(t, report{status: acp.ToolCallStatusFailed, content: texts(text)})
	}
	if c := ts.compact; c != nil && (c.ended || exited) {
		ts.compact = nil
		if !c.ended {
			c.ended = true
			ts.sendLocked(c, report{status: acp.ToolCallStatusFailed, content: texts(exitedText)})
		}
	}
	delete(ts.streaming, "")
	return incomplete
}

// stoppedText is what a call of a subagent that ended says when it had
// no result.
const stoppedText = "The subagent stopped before this call had a result."

// endSubagent ends the open calls of the subagent agent, whose stream is
// one of calls.
func (ts *tools) endSubagent(agent string, calls map[string]bool) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	for _, id := range slices.Sorted(maps.Keys(ts.byID)) {
		if t := ts.byID[id]; !t.ended && (calls[t.parent] || t.parent == "" && t.agent == agent) {
			t.ended = true
			ts.sendLocked(t, report{status: acp.ToolCallStatusFailed, content: texts(stoppedText)})
		}
	}
}
