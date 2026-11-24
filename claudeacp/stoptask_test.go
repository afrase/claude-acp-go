package claudeacp

import (
	"strings"
	"testing"

	acp "github.com/afrase/acp-go/unstable"

	"github.com/afrase/claude-acp-go/claudeagent/claudetest"
)

// A task is stopped with claude's stop_task, while a turn runs and with no
// turn of its own: the answer is empty, and the task's end comes as
// claude tells it, on the call that started it.
func TestStopTask(t *testing.T) {
	taken, release := make(chan struct{}), make(chan struct{})
	f := fake(user("id2"), echo("id2"),
		assistant(`{"type":"tool_use","id":"toolu_A","name":"Bash","input":{"command":"sleep 9","run_in_background":true}}`),
		sendf(`{"type":"system","subtype":"task_started","task_id":"bA","tool_use_id":"toolu_A","task_type":"local_bash","is_backgrounded":true}`),
		sendf(`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_A","content":"Command running in background with ID: bA."}]},`+
			`"tool_use_result":{"backgroundTaskId":"bA"}}`),
		signal(taken), claudetest.Do(func() { <-release }),
		sendf(`{"type":"system","subtype":"task_updated","task_id":"bA","patch":{"status":"killed","end_time":7}}`),
		sendf(`{"type":"system","subtype":"task_notification","task_id":"bA","tool_use_id":"toolu_A","status":"stopped","summary":"Sleep"}`),
		result(names("id2")), claudetest.Send(idle))
	f.Respond(`{"type":"control_request","request":{"subtype":"stop_task","task_id":"bA"}}`, controlOK)
	p, sid := startOpen(t, nil, f)
	w := p.prompt(t, sid, "go")
	wait(t, taken, "the command started")

	var r StopTaskResponse
	if err := p.Call(t.Context(), StopTaskMethod, StopTaskRequest{SessionID: string(sid), TaskID: "bA"}, &r); err != nil {
		t.Fatal(err)
	}
	if got := asked(f); len(got) != 1 || !strings.Contains(got[0], `"subtype":"stop_task","task_id":"bA"`) {
		t.Fatalf("asked %q", got)
	}
	close(release)
	stopsWith(t, w, string(acp.StopReasonEndTurn))
	var statuses []string
	for _, n := range p.notes() {
		if u := n.Update.ToolCallUpdate; u != nil {
			if task, ok := claudeMeta(u.Meta)["task"].(map[string]any); ok {
				statuses = append(statuses, string(u.ToolCallID)+" "+str(task["status"]))
			}
		}
	}
	sameLines(t, statuses, []string{"toolu_A running", "toolu_A killed", "toolu_A stopped"})
}

// claude's refusal (a task it doesn't know, say) is the call's error, as
// claude worded it; no task id, or a session that isn't open, is refused
// before claude is asked.
func TestStopTaskRefused(t *testing.T) {
	f := fake()
	f.Respond(`{"type":"control_request","request":{"subtype":"stop_task"}}`,
		`{"type":"control_response","response":{"subtype":"error","request_id":"$req","error":"No task found with ID: nope"}}`)
	p, sid := startOpen(t, nil, f)
	var r StopTaskResponse
	err := p.Call(t.Context(), StopTaskMethod, StopTaskRequest{SessionID: string(sid), TaskID: "nope"}, &r)
	if err == nil || !strings.Contains(err.Error(), "No task found with ID: nope") {
		t.Errorf("claude's refusal: %v", err)
	}
	for _, req := range []StopTaskRequest{{SessionID: string(sid), TaskID: " "}, {SessionID: "nope", TaskID: "bA"}} {
		if err := p.Call(t.Context(), StopTaskMethod, req, &r); err == nil {
			t.Errorf("%+v answered", req)
		}
	}
	if got := asked(f); len(got) != 1 {
		t.Errorf("asked %q", got)
	}
}
