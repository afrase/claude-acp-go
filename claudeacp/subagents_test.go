package claudeacp

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	acp "github.com/afrase/acp-go/unstable"

	"github.com/afrase/claude-acp-go/claudeagent/claudetest"
)

// withSubagents is a client that takes subagents as sessions of their own.
var withSubagents = &acp.ClientCapabilities{Subagents: &acp.SubagentCapabilities{}}

// agentCall is the main agent starting a subagent with the call toolu_p.
func agentCall(background bool) claudetest.Step {
	return assistant(fmt.Sprintf(`{"type":"tool_use","id":"toolu_p","name":"Agent","input":{"name":"counter","description":"Count",`+
		`"subagent_type":"general-purpose","prompt":"count to 3","run_in_background":%v}}`, background))
}

// taskStarted is claude starting the subagent task a1 for toolu_p; fields
// are more of its fields.
func taskStarted(fields string) claudetest.Step {
	return sendf(`{"type":"system","subtype":"task_started","task_id":"a1","tool_use_id":"toolu_p",` +
		`"task_type":"local_agent","subagent_type":"general-purpose","description":"Count"` + fields + `}`)
}

// subagentCall is the subagent of call calling Bash as id.
func subagentCall(call, id string) claudetest.Step {
	return sendf(`{"type":"assistant","parent_tool_use_id":"` + call + `","message":{"id":"msg_s","content":[{"type":"tool_use","id":"` +
		id + `","name":"Bash","input":{"command":"ls"}}]}}`)
}

// agentDone is toolu_p's result.
var agentDone = sendf(`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_p","content":"done"}]}}`)

// taskEnd is claude's notification of a1's end.
func taskEnd(status string) claudetest.Step {
	return sendf(`{"type":"system","subtype":"task_notification","task_id":"a1","status":"` + status + `","summary":"it broke"}`)
}

// states are the states the client was told of the subagent a1, after
// its announcement.
func (c *client) states() []string {
	var out []string
	for _, n := range c.notes() {
		if su := n.Update.SubagentUpdate; su != nil && su.SessionID == "a1" && !su.Title.Present {
			b, _ := json.Marshal(su.State)
			out = append(out, string(b))
		}
	}
	return out
}

const (
	stateRunning = `{"state":"running"}`
	stateAsking  = `{"state":"requires_action"}`
)

// A subagent is a session of its own: announced on the session with its
// call's name, role and description, its first message the call's
// prompt, under the call's id; its work, from before claude names it
// too, goes there, still naming the call; at its end it is idle, and its
// calls still open end. The Agent call stays on the session.
func TestSubagentSession(t *testing.T) {
	f := fake(user("id2"), echo("id2"), agentCall(false), subagentCall("toolu_p", "toolu_1"),
		taskStarted(`,"prompt":"count to 3"`), subagentCall("toolu_p", "toolu_2"),
		sendf(`{"type":"user","parent_tool_use_id":"toolu_p","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"a"}]}}`),
		taskEnd("completed"),
		sendf(`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_p","content":"3"}]}}`),
		result(names("id2")), claudetest.Send(idle))
	p := start(t, withSubagents, f)
	sid := p.open(t)
	if got := stopReason(p.prompt(t, sid, "go")); got != "end_turn" {
		t.Fatal(got)
	}
	got := shape(p.notes(), sid)
	want := []string{
		"s1 call toolu_p Agent pending",
		`s1 subagent {"sessionId":"a1","title":"counter (general-purpose)","description":"Count","capabilities":{"cancel":{}},"state":{"state":"running"}}`,
		"a1 message toolu_p to a1",
		"a1 call toolu_1 Bash pending parent=toolu_p",
		"a1 call toolu_2 Bash pending parent=toolu_p",
		"a1 update toolu_1 completed parent=toolu_p",
		`s1 subagent {"sessionId":"a1","state":{"state":"idle","stopReason":"end_turn"}}`,
		"a1 update toolu_2 failed parent=toolu_p",
		"s1 update toolu_p completed",
		"s1 usage human",
	}
	if !slices.Equal(got, want) {
		t.Errorf("got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	m := p.of("session_message")[0].SessionMessage
	if c := m.Content.Value; c == nil || len(*c) != 1 || (*c)[0].Text.Text != "count to 3" {
		t.Errorf("the first message: %+v", m)
	}
}

// A client without subagents sees a subagent's work on the session, each
// update naming its call, and no child.
func TestSubagentWithoutSessions(t *testing.T) {
	f := fake(user("id2"), echo("id2"), agentCall(false), taskStarted(""),
		subagentCall("toolu_p", "toolu_1"), taskEnd("completed"), agentDone, result(names("id2")), claudetest.Send(idle))
	p := start(t, nil, f)
	sid := p.open(t)
	if got := stopReason(p.prompt(t, sid, "go")); got != "end_turn" {
		t.Fatal(got)
	}
	got := shape(p.notes(), sid)
	want := []string{"s1 call toolu_p Agent pending", "s1 call toolu_1 Bash pending parent=toolu_p",
		"s1 update toolu_1 failed parent=toolu_p", "s1 update toolu_p completed", "s1 usage human"}
	if !slices.Equal(got, want) {
		t.Errorf("got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// A subagent ends as claude's task does: done, cancelled (stopped or
// killed), failed with claude's text, or out of sight; once a run, and
// not at a late frame of a run before.
func TestSubagentEnds(t *testing.T) {
	for _, tc := range []struct {
		name   string
		frames []string
		want   []string
	}{
		{"completed", []string{`"subtype":"task_notification","task_id":"a1","status":"completed"`},
			[]string{`{"state":"idle","stopReason":"end_turn"}`}},
		{"killed then stopped", []string{`"subtype":"task_updated","task_id":"a1","patch":{"status":"killed"}`,
			`"subtype":"task_notification","task_id":"a1","status":"stopped"`},
			[]string{`{"state":"idle","stopReason":"cancelled"}`}},
		{"failed", []string{`"subtype":"task_updated","task_id":"a1","patch":{"status":"failed","error":"it broke"}`},
			[]string{`{"state":"idle","stopReason":"error","error":{"code":-32603,"message":"it broke"}}`}},
		{"disconnected", []string{`"subtype":"task_notification","task_id":"a1","status":"disconnected"`},
			[]string{`{"state":"unknown"}`}},
		{"still running", []string{`"subtype":"task_updated","task_id":"a1","patch":{"status":"running","is_backgrounded":true}`}, nil},
		{"a late run", []string{`"subtype":"task_notification","task_id":"a1","run_id":"r0","status":"failed"`,
			`"subtype":"task_notification","task_id":"a1","run_id":"r1","status":"completed"`},
			[]string{`{"state":"idle","stopReason":"end_turn"}`}},
		{"another task", []string{`"subtype":"task_notification","task_id":"b2","status":"completed"`}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			steps := []claudetest.Step{user("id2"), echo("id2"), agentCall(true), taskStarted(`,"run_id":"r1"`)}
			for _, fr := range tc.frames {
				steps = append(steps, sendf(`{"type":"system",`+fr+`}`))
			}
			steps = append(steps, agentDone, result(names("id2")), claudetest.Send(idle))
			p := start(t, withSubagents, fake(steps...))
			if got := stopReason(p.prompt(t, p.open(t), "go")); got != "end_turn" {
				t.Fatal(got)
			}
			if got := p.states(); !slices.Equal(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// A subagent's work before claude names its subagent waits within bounds:
// past them it goes on the session, and so do the later updates of the
// tool calls that went there; and a call that ends with claude
// never naming a subagent has its work go there too.
func TestSubagentHeldBounds(t *testing.T) {
	steps := []claudetest.Step{user("id2"), echo("id2"), agentCall(true)}
	for i := range maxHeldPerCall + 8 {
		steps = append(steps, subagentCall("toolu_p", fmt.Sprintf("toolu_%d", i)))
	}
	// The first call's tool_call went on the session: its result follows it.
	steps = append(steps, taskStarted(""),
		sendf(`{"type":"user","parent_tool_use_id":"toolu_p","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_0","content":"a"}]}}`),
		agentDone,
		assistant(`{"type":"tool_use","id":"toolu_q","name":"Agent","input":{}}`), subagentCall("toolu_q", "toolu_x"),
		sendf(`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_q","content":"done"}]}}`),
		result(names("id2")), claudetest.Send(idle))
	p := start(t, withSubagents, fake(steps...))
	sid := p.open(t)
	if got := stopReason(p.prompt(t, sid, "go")); got != "end_turn" {
		t.Fatal(got)
	}
	on := map[string]int{}
	for _, n := range p.notes() {
		if tc := n.Update.ToolCall; tc != nil && tc.Name == "Bash" {
			on[string(n.SessionID)]++
		}
	}
	if on[string(sid)] != maxHeldPerCall+2 || on["a1"] != 7 {
		t.Errorf("calls by session: %v", on)
	}
	ended := false
	for _, n := range p.notes() {
		if u := n.Update.ToolCallUpdate; u != nil && u.ToolCallID == "toolu_0" && u.Status == "completed" {
			if ended = n.SessionID == sid; !ended {
				t.Errorf("toolu_0's tool_call is on the session, its end on %s", n.SessionID)
			}
		}
	}
	if !ended {
		t.Error("toolu_0 never ended on the session")
	}
}

// A subagent's permission is asked on its session, which requires action
// meanwhile. A cancel of the session stops that subagent alone, through
// claude, and withdraws what it asks; a cancel of the turn spares a
// background subagent's request.
func TestSubagentAsks(t *testing.T) {
	ask := canUseTool("toolu_s", "Bash", `{"command":"ls"}`, `,"agent_id":"a1"`)
	t.Run("answered", func(t *testing.T) {
		p := start(t, withSubagents, fake(user("id2"), echo("id2"), agentCall(false), taskStarted(""),
			ask, answers("toolu_s", `{"behavior":"allow"}`), agentDone, result(names("id2")), claudetest.Send(idle)))
		var asked acp.SessionID
		p.permit = func(ctx context.Context, req *acp.RequestPermissionRequest) (*acp.RequestPermissionResponse, error) {
			asked = req.SessionID
			return pick(optAllowOnce)(ctx, req)
		}
		if got := stopReason(p.prompt(t, p.open(t), "go")); got != "end_turn" {
			t.Fatal(got)
		}
		if got := p.states(); asked != "a1" || !slices.Equal(got, []string{stateAsking, stateRunning}) {
			t.Errorf("asked on %q; states %v", asked, got)
		}
	})
	t.Run("its session cancelled", func(t *testing.T) {
		// The request's answer and stop_task go apart, in either order.
		f := fake(user("id2"), echo("id2"), agentCall(false), taskStarted(""), ask,
			answers("toolu_s", `{"behavior":"deny","interrupt":true}`), taskEnd("stopped"), agentDone,
			result(names("id2")), claudetest.Send(idle))
		f.Respond(`{"type":"control_request","request":{"subtype":"stop_task"}}`,
			`{"type":"control_response","response":{"subtype":"success","request_id":"$req","response":{}}}`)
		p := start(t, withSubagents, f)
		opened := make(chan struct{})
		p.permit = func(ctx context.Context, _ *acp.RequestPermissionRequest) (*acp.RequestPermissionResponse, error) {
			close(opened)
			<-ctx.Done()
			return nil, ctx.Err()
		}
		w := p.prompt(t, p.open(t), "go")
		wait(t, opened, "the request")
		if err := p.Cancel(context.Background(), &acp.CancelNotification{SessionID: "a1"}); err != nil {
			t.Fatal(err)
		}
		if got := stopReason(w); got != "end_turn" {
			t.Fatal(got)
		}
		if got := p.states(); !slices.Equal(got, []string{stateAsking, `{"state":"idle","stopReason":"cancelled"}`}) {
			t.Errorf("states %v", got)
		}
		eventually(t, "stop_task", func() bool { return len(asked(f)) > 0 })
		if got := asked(f); !strings.Contains(got[0], `"subtype":"stop_task","task_id":"a1"`) {
			t.Errorf("claude was asked %v", got)
		}
	})
	t.Run("the turn cancelled", func(t *testing.T) {
		opened, release := make(chan struct{}), make(chan struct{})
		f := fake(user("id2"), echo("id2"), agentCall(true), taskStarted(`,"is_backgrounded":true`), agentDone, ask,
			claudetest.Until(release), result(names("id2")), claudetest.Send(idle),
			answers("toolu_s", `{"behavior":"allow"}`))
		p := start(t, withSubagents, f)
		withdrawn := make(chan bool, 1)
		p.permit = func(ctx context.Context, req *acp.RequestPermissionRequest) (*acp.RequestPermissionResponse, error) {
			close(opened)
			<-release
			withdrawn <- ctx.Err() != nil
			return pick(optAllowOnce)(ctx, req)
		}
		sid := p.open(t)
		w := p.prompt(t, sid, "go")
		wait(t, opened, "the request")
		if err := p.Cancel(context.Background(), &acp.CancelNotification{SessionID: sid}); err != nil {
			t.Fatal(err)
		}
		eventually(t, "the interrupt", func() bool { return len(asked(f)) > 0 })
		close(release)
		if got := stopReason(w); got != "cancelled" {
			t.Fatal(got)
		}
		if <-withdrawn {
			t.Error("the turn's cancel withdrew a background subagent's request")
		}
	})
}

// Stopping a subagent stops the foreground subagents it waits on too:
// what they ask is withdrawn with it, so they don't show running again
// while they are being stopped.
func TestSubagentStopNested(t *testing.T) {
	release := make(chan struct{})
	f := fake(user("id2"), echo("id2"), agentCall(false), taskStarted(""),
		sendf(`{"type":"assistant","parent_tool_use_id":"toolu_p","message":{"id":"msg_s","content":[{"type":"tool_use","id":"toolu_q","name":"Agent",`+
			`"input":{"description":"Inner","prompt":"go"}}]}}`),
		sendf(`{"type":"system","subtype":"task_started","task_id":"b2","tool_use_id":"toolu_q","parent_task_id":"a1","task_type":"local_agent","subagent_type":"general-purpose"}`),
		canUseTool("toolu_s", "Bash", `{"command":"ls"}`, `,"agent_id":"b2"`),
		// claude stops b2 with a1, withdrawing its request itself.
		claudetest.Until(release),
		sendf(`{"type":"control_cancel_request","request_id":"p_toolu_s"}`),
		sendf(`{"type":"system","subtype":"task_notification","task_id":"b2","status":"stopped"}`),
		taskEnd("stopped"), agentDone, result(names("id2")), claudetest.Send(idle))
	f.Respond(`{"type":"control_request","request":{"subtype":"stop_task"}}`,
		`{"type":"control_response","response":{"subtype":"success","request_id":"$req","response":{}}}`)
	f.Respond(`{"type":"control_response","response":{"request_id":"p_toolu_s"}}`, `{"type":"keep_alive"}`)
	p := start(t, withSubagents, f)
	opened := make(chan struct{})
	p.permit = func(ctx context.Context, _ *acp.RequestPermissionRequest) (*acp.RequestPermissionResponse, error) {
		close(opened)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	w := p.prompt(t, p.open(t), "go")
	wait(t, opened, "the request")
	if err := p.Cancel(context.Background(), &acp.CancelNotification{SessionID: "a1"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "stop_task", func() bool { return len(asked(f)) > 0 })
	close(release)
	if got := stopReason(w); got != "end_turn" {
		t.Fatal(got)
	}
	var states []string
	for _, n := range p.notes() {
		if su := n.Update.SubagentUpdate; su != nil && su.SessionID == "b2" && !su.Title.Present {
			b, _ := json.Marshal(su.State)
			states = append(states, string(b))
		}
	}
	if want := []string{stateAsking, `{"state":"idle","stopReason":"cancelled"}`}; !slices.Equal(states, want) {
		t.Errorf("b2's states %v, want %v", states, want)
	}
}

// A subagent that ended runs again for a SendMessage that resumed it,
// whose message is its next; or when claude says it runs, with no
// message to show.
func TestSubagentAgain(t *testing.T) {
	sendMessage := []claudetest.Step{
		assistant(`{"type":"tool_use","id":"toolu_m","name":"SendMessage","input":{"to":"a1","message":"count again"}}`),
		sendf(`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_m","content":"sent"}]},` +
			`"tool_use_result":{"success":true,"resumedAgentId":"a1"}}`),
		subagentCall("toolu_m", "toolu_2"),
	}
	for _, tc := range []struct {
		name  string
		again []claudetest.Step
		want  []string
	}{
		{"SendMessage", sendMessage, []string{
			"s1 call toolu_m SendMessage pending",
			"s1 update toolu_m completed",
			`s1 subagent {"sessionId":"a1","state":{"state":"running"}}`,
			"a1 message toolu_m to a1",
			"a1 call toolu_2 Bash pending parent=toolu_m",
		}},
		// The result can come after the run it started has ended: then it
		// runs nothing.
		{"SendMessage ended", []claudetest.Step{sendMessage[0],
			sendf(`{"type":"system","subtype":"task_started","task_id":"a1","run_id":"r2","tool_use_id":"toolu_m","task_type":"local_agent"}`),
			subagentCall("toolu_m", "toolu_2"),
			sendf(`{"type":"system","subtype":"task_notification","task_id":"a1","run_id":"r2","tool_use_id":"toolu_m","status":"completed"}`),
			sendMessage[1]}, []string{
			"s1 call toolu_m SendMessage pending",
			`s1 subagent {"sessionId":"a1","state":{"state":"running"}}`,
			"a1 message toolu_m to a1",
			"a1 call toolu_2 Bash pending parent=toolu_m",
			`s1 subagent {"sessionId":"a1","state":{"state":"idle","stopReason":"end_turn"}}`,
			"a1 update toolu_2 failed parent=toolu_m",
			"s1 update toolu_m completed",
		}},
		{"running", []claudetest.Step{sendf(`{"type":"system","subtype":"task_updated","task_id":"a1","patch":{"status":"running"}}`)}, []string{
			`s1 subagent {"sessionId":"a1","state":{"state":"running"}}`,
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			steps := []claudetest.Step{user("id2"), echo("id2"), agentCall(true), taskStarted(""), agentDone,
				taskEnd("completed")}
			steps = append(append(steps, tc.again...), result(names("id2")), claudetest.Send(idle))
			p := start(t, withSubagents, fake(steps...))
			sid := p.open(t)
			if got := stopReason(p.prompt(t, sid, "go")); got != "end_turn" {
				t.Fatal(got)
			}
			got := shape(p.notes(), sid)
			end := slices.Index(got, `s1 subagent {"sessionId":"a1","state":{"state":"idle","stopReason":"end_turn"}}`)
			if end < 0 || !slices.Equal(got[end+1:len(got)-1], tc.want) {
				t.Errorf("got:\n%s\nwant after its end:\n%s", strings.Join(got, "\n"), strings.Join(tc.want, "\n"))
			}
			if tc.name != "SendMessage" {
				return
			}
			ms := p.of("session_message")
			if m := ms[len(ms)-1].SessionMessage; m.Content.Value == nil || (*m.Content.Value)[0].Text.Text != "count again" {
				t.Errorf("its message: %+v", m)
			}
		})
	}
}

// A resumed subagent keeps its task id and gets a newer run id, and may
// get no task_started: a running update or a SendMessage's result is all
// that says it runs. The newer run's frames are its run now, and its end
// ends it; a start of a run before it is late.
func TestSubagentNewerRun(t *testing.T) {
	sendMessage := []claudetest.Step{
		assistant(`{"type":"tool_use","id":"toolu_m","name":"SendMessage","input":{"to":"a1","message":"count again"}}`),
		sendf(`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_m","content":"sent"}]},` +
			`"tool_use_result":{"success":true,"resumedAgentId":"a1"}}`),
	}
	running := sendf(`{"type":"system","subtype":"task_updated","task_id":"a1","run_id":"r2","patch":{"status":"running"}}`)
	ends := sendf(`{"type":"system","subtype":"task_notification","task_id":"a1","run_id":"r2","tool_use_id":"toolu_m","status":"completed"}`)
	const endTurn = `{"state":"idle","stopReason":"end_turn"}`
	for _, tc := range []struct {
		name   string
		again  []claudetest.Step
		states []string
		call   string // a line shape must have
		absent string // and one it mustn't
	}{
		{"running", []claudetest.Step{running,
			sendf(`{"type":"system","subtype":"task_notification","task_id":"a1","run_id":"r2","status":"completed"}`)},
			[]string{endTurn, stateRunning, endTurn}, "", ""},
		{"SendMessage", append(slices.Clone(sendMessage), running, subagentCall("toolu_m", "toolu_2"), ends),
			[]string{endTurn, stateRunning, endTurn}, "a1 update toolu_2 failed parent=toolu_m", ""},
		{"a late start", []claudetest.Step{running,
			sendf(`{"type":"system","subtype":"task_started","task_id":"a1","run_id":"r1","tool_use_id":"toolu_old","task_type":"local_agent"}`),
			sendf(`{"type":"system","subtype":"task_notification","task_id":"a1","run_id":"r2","status":"failed"}`)},
			[]string{endTurn, stateRunning, `{"state":"idle","stopReason":"error"}`}, "", "a1 message toolu_old to a1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			steps := []claudetest.Step{user("id2"), echo("id2"), agentCall(true), taskStarted(`,"run_id":"r1"`), agentDone,
				sendf(`{"type":"system","subtype":"task_notification","task_id":"a1","run_id":"r1","status":"completed"}`)}
			steps = append(append(steps, tc.again...), result(names("id2")), claudetest.Send(idle))
			p := start(t, withSubagents, fake(steps...))
			sid := p.open(t)
			if got := stopReason(p.prompt(t, sid, "go")); got != "end_turn" {
				t.Fatal(got)
			}
			if got := p.states(); !slices.Equal(got, tc.states) {
				t.Errorf("states %v, want %v", got, tc.states)
			}
			if got := shape(p.notes(), sid); tc.call != "" && !slices.Contains(got, tc.call) {
				t.Errorf("no %q in:\n%s", tc.call, strings.Join(got, "\n"))
			}
			if got := shape(p.notes(), sid); tc.absent != "" && slices.Contains(got, tc.absent) {
				t.Errorf("%q in:\n%s", tc.absent, strings.Join(got, "\n"))
			}
		})
	}
}

// Without subagent sessions, a subagent that ended runs again all the
// same: the end of its next run ends the calls that run left open, and a
// cancel of the turn spares it while it is in the background.
func TestSubagentAgainWithoutSessions(t *testing.T) {
	before := []claudetest.Step{user("id2"), echo("id2"), agentCall(true), taskStarted(`,"is_backgrounded":true`), agentDone,
		taskEnd("completed"),
		assistant(`{"type":"tool_use","id":"toolu_m","name":"SendMessage","input":{"to":"a1","message":"count again"}}`),
		sendf(`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_m","content":"sent"}]},` +
			`"tool_use_result":{"success":true,"resumedAgentId":"a1"}}`),
	}
	t.Run("its next end", func(t *testing.T) {
		steps := append(slices.Clone(before), subagentCall("toolu_m", "toolu_2"),
			sendf(`{"type":"system","subtype":"task_notification","task_id":"a1","tool_use_id":"toolu_m","status":"stopped"}`),
			result(names("id2")), claudetest.Send(idle))
		p := start(t, nil, fake(steps...))
		sid := p.open(t)
		if got := stopReason(p.prompt(t, sid, "go")); got != "end_turn" {
			t.Fatal(got)
		}
		got := shape(p.notes(), sid)
		want := []string{"s1 call toolu_2 Bash pending parent=toolu_m", "s1 update toolu_2 failed parent=toolu_m", "s1 usage human"}
		if i := slices.Index(got, want[0]); i < 0 || !slices.Equal(got[i:], want) {
			t.Errorf("got:\n%s\nwant at its end:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
	})
	t.Run("the turn cancelled", func(t *testing.T) {
		opened, release := make(chan struct{}), make(chan struct{})
		steps := append(slices.Clone(before), canUseTool("toolu_s", "Bash", `{"command":"ls"}`, `,"agent_id":"a1"`),
			claudetest.Until(release), result(names("id2")), claudetest.Send(idle),
			answers("toolu_s", `{"behavior":"allow"}`))
		f := fake(steps...)
		p := start(t, nil, f)
		withdrawn := make(chan bool, 1)
		p.permit = func(ctx context.Context, req *acp.RequestPermissionRequest) (*acp.RequestPermissionResponse, error) {
			close(opened)
			<-release
			withdrawn <- ctx.Err() != nil
			return pick(optAllowOnce)(ctx, req)
		}
		sid := p.open(t)
		w := p.prompt(t, sid, "go")
		wait(t, opened, "the request")
		if err := p.Cancel(context.Background(), &acp.CancelNotification{SessionID: sid}); err != nil {
			t.Fatal(err)
		}
		// The interrupt goes after the withdrawal, which then has had time
		// to reach the client.
		eventually(t, "the interrupt", func() bool { return len(asked(f)) > 0 })
		close(release)
		if got := stopReason(w); got != "cancelled" {
			t.Fatal(got)
		}
		if <-withdrawn {
			t.Error("the turn's cancel withdrew a resumed background subagent's request")
		}
	})
}

// When claude exits, a subagent still running ends with no reason given.
func TestSubagentsGone(t *testing.T) {
	p := start(t, withSubagents, fake(user("id2"), echo("id2"), agentCall(true), taskStarted(""), agentDone,
		claudetest.Exit(1, "")))
	if got := stopReason(p.prompt(t, p.open(t), "go")); !strings.HasPrefix(got, "error") {
		t.Fatal(got)
	}
	eventually(t, "its end", func() bool { return len(p.states()) > 0 })
	if got := p.states(); !slices.Equal(got, []string{`{"state":"idle"}`}) {
		t.Errorf("%v", got)
	}
}

// shape is what a client of subagents goes by in notes, a line each:
// which session each update is on, the tool calls with their parents and
// statuses, subagents' announcements, states and first messages, text
// (a run of chunks as one), usage by origin, and what hooks' responses
// say of tasks. root is the session's id, written s1.
func shape(notes []acp.SessionNotification, root acp.SessionID) (lines []string) {
	for _, n := range notes {
		sid := string(n.SessionID)
		if n.SessionID == root {
			sid = "s1"
		}
		u := n.Update
		var line string
		switch {
		case u.AgentMessageChunk != nil:
			line = sid + " text"
			if len(lines) > 0 && lines[len(lines)-1] == line {
				continue
			}
		case u.UsageUpdate != nil:
			origin, _ := u.UsageUpdate.Meta["_claude/origin"].(map[string]any)
			line = sid + " usage " + str(origin["kind"])
		case u.SubagentUpdate != nil:
			b, _ := json.Marshal(u.SubagentUpdate)
			line = sid + " subagent " + string(b)
		case u.SessionMessage != nil:
			line = sid + " message " + string(u.SessionMessage.MessageID) + " to " + string(*u.SessionMessage.RecipientSessionID.Value)
		case u.ToolCall != nil:
			cc := claudeMeta(u.ToolCall.Meta)
			line = sid + " call " + string(u.ToolCall.ToolCallID) + " " + str(cc["toolName"]) + " " + string(u.ToolCall.Status) + parentOf(cc)
		case u.ToolCallUpdate != nil:
			cc := claudeMeta(u.ToolCallUpdate.Meta)
			id := string(u.ToolCallUpdate.ToolCallID)
			if r, ok := cc["toolResponse"].(map[string]any); ok {
				line = sid + " response " + id + " agentId=" + str(r["agentId"]) + " backgroundTaskId=" + str(r["backgroundTaskId"]) + parentOf(cc)
				break
			}
			if u.ToolCallUpdate.Status == "" {
				continue
			}
			line = sid + " update " + id + " " + string(u.ToolCallUpdate.Status) + parentOf(cc)
		default:
			continue
		}
		lines = append(lines, line)
	}
	return lines
}

func claudeMeta(meta map[string]any) map[string]any {
	cc, _ := meta["claudeCode"].(map[string]any)
	return cc
}

func parentOf(cc map[string]any) string {
	if p := str(cc["parentToolUseId"]); p != "" {
		return " parent=" + p
	}
	return ""
}

func str(v any) string { s, _ := v.(string); return s }

// notes are the notifications so far.
func (c *client) notes() []acp.SessionNotification {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.updates)
}

// A stop reaches the subagent and the foreground ones under it, through
// foreground ones only.
func TestSubagentStopsWith(t *testing.T) {
	var k subagents
	k.init("s1", true, func(string, acp.SessionUpdate) {})
	for _, sa := range []*subagent{{id: "a"}, {id: "b", parent: "a"}, {id: "c", parent: "b"},
		{id: "bg", parent: "a", background: true}, {id: "d", parent: "bg"}} {
		k.byID[sa.id] = sa
	}
	for _, tc := range []struct {
		agent, id string
		want      bool
	}{{"a", "a", true}, {"b", "a", true}, {"c", "a", true}, {"bg", "a", false}, {"d", "a", false},
		{"bg", "bg", true}, {"d", "bg", true}, {"a", "b", false}, {"x", "a", false}} {
		if got := k.stopsWith(tc.agent, tc.id); got != tc.want {
			t.Errorf("stopsWith(%s, %s) = %v", tc.agent, tc.id, got)
		}
	}
}

// After session/resume, a SendMessage can resume a subagent that ran
// before claude was restarted: its history names the call that started
// it, which claude sends the run's work under, so the work goes to the
// subagent's session.
func TestSubagentResumedAfterRestart(t *testing.T) {
	f := fake(user("id1"), echo("id1"),
		assistant(`{"type":"tool_use","id":"toolu_m","name":"SendMessage","input":{"to":"a1","message":"count again"}}`),
		subagentCall("toolu_A", "toolu_2"),
		sendf(`{"type":"system","subtype":"task_started","task_id":"a1","run_id":"r2","tool_use_id":"toolu_m","task_type":"local_agent","prompt":"count again"}`),
		sendf(`{"type":"user","parent_tool_use_id":"toolu_A","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_2","content":"1"}]}}`),
		sendf(`{"type":"system","subtype":"task_notification","task_id":"a1","run_id":"r2","tool_use_id":"toolu_m","status":"completed"}`),
		sendf(`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_m","content":"sent"}]},`+
			`"tool_use_result":{"success":true,"resumedAgentId":"a1"}}`),
		result(names("id1")), claudetest.Send(idle))
	p := start(t, withSubagents, f)
	cwd, sid := t.TempDir(), nthUUID(1)
	p.writeHistory(t, cwd, sid, userRec("u1", "Count"),
		asstRec("a1", "msg_1", useBlock("toolu_A", "Agent", `{"description":"Count","prompt":"count","run_in_background":true}`)),
		userRec("r1", resultBlocks("toolu_A", "Async agent launched", false)))
	p.writeSubagent(t, cwd, sid, "a1", "toolu_A", userRec("s1", "count"), asstRec("s2", "msg_A1", textBlock("1")))
	if _, err := p.ResumeSession(context.Background(), &acp.ResumeSessionRequest{SessionID: acp.SessionID(sid), Cwd: cwd}); err != nil {
		t.Fatal(err)
	}
	if got := stopReason(p.prompt(t, acp.SessionID(sid), "go")); got != "end_turn" {
		t.Fatal(got)
	}
	got := shape(p.notes(), acp.SessionID(sid))
	want := []string{
		"s1 call toolu_m SendMessage pending",
		`s1 subagent {"sessionId":"a1","capabilities":{"cancel":{}},"state":{"state":"running"}}`,
		"a1 message toolu_m to a1",
		"a1 call toolu_2 Bash pending parent=toolu_A",
		"a1 update toolu_2 completed parent=toolu_A",
		`s1 subagent {"sessionId":"a1","state":{"state":"idle","stopReason":"end_turn"}}`,
		"s1 update toolu_m completed",
		"s1 usage human",
	}
	if !slices.Equal(got, want) {
		t.Errorf("got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
