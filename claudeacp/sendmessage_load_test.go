package claudeacp

import (
	"slices"
	"strings"
	"testing"

	acp "github.com/afrase/acp-go/unstable"

	"github.com/afrase/claude-acp-go/claudeagent/claudetest"
)

// sentRec is the result of the SendMessage call id, as claude keeps it:
// toolUseResult says whether it ran a subagent again, and which.
func sentRec(uuid, id string, success bool, agent string) rec {
	return userRec(uuid, resultBlocks(id, "sent", false), "toolUseResult", map[string]any{"success": success, "resumedAgentId": agent})
}

// A load replays a SendMessage that ran a subagent again: after the
// call's end, as live, the subagent is delegated the message under that
// call, on its own session;
// one that didn't run it (it failed, or names a subagent the history
// doesn't have) delegates nothing. The subagent ends as its own call
// did. A SendMessage after the load runs it again on the same session.
func TestLoadReplaysSendMessage(t *testing.T) {
	steps := []claudetest.Step{user("id1"), echo("id1"),
		assistant(`{"type":"tool_use","id":"toolu_N","name":"SendMessage","input":{"to":"agentA","message":"and again"}}`),
		sendf(`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_N","content":"sent"}]},` +
			`"tool_use_result":{"success":true,"resumedAgentId":"agentA"}}`),
		subagentCall("toolu_N", "toolu_n1"),
		result(names("id1")), claudetest.Send(idle)}
	p := start(t, withSubagents, fake(steps...))
	cwd, sid := t.TempDir(), nthUUID(1)
	send := func(id, message string) map[string]any {
		return useBlock(id, "SendMessage", `{"to":"agentA","message":"`+message+`"}`)
	}
	p.writeHistory(t, cwd, sid,
		userRec("u1", "Count"),
		asstRec("a1", "msg_1", useBlock("toolu_A", "Agent", `{"description":"Count","prompt":"count"}`)),
		userRec("r1", resultBlocks("toolu_A", "1", false)),
		asstRec("a2", "msg_2", send("toolu_M", "count again")),
		sentRec("r2", "toolu_M", true, "agentA"),
		asstRec("a3", "msg_3", send("toolu_F", "fail")),
		sentRec("r3", "toolu_F", false, "agentA"),
		asstRec("a4", "msg_4", send("toolu_X", "nobody")),
		sentRec("r4", "toolu_X", true, "agentX"),
		asstRec("a5", "msg_5", textBlock("Done.")),
	)
	p.writeSubagent(t, cwd, sid, "agentA", "toolu_A",
		userRec("s1", "count"),
		asstRec("s2", "msg_A1", textBlock("1")),
		userRec("s3", "count again"),
		asstRec("s4", "msg_A2", textBlock("2")),
	)
	p.mustLoad(t, sid, cwd)
	var got []string
	for _, l := range p.replayed() {
		if strings.Contains(l, "toolu_M") || strings.Contains(l, "toolu_F") || strings.Contains(l, "toolu_X") || strings.Contains(l, `"state":{"state":"idle"`) {
			got = append(got, l)
		}
	}
	want := []string{
		sid + " call toolu_M SendMessage pending",
		sid + " update toolu_M completed",
		"agentA message toolu_M",
		sid + " call toolu_F SendMessage pending",
		sid + " update toolu_F completed",
		sid + " call toolu_X SendMessage pending",
		sid + " update toolu_X completed",
		sid + " subagent " + `{"sessionId":"agentA","state":{"state":"idle","stopReason":"end_turn"}}`,
	}
	if !slices.Equal(got, want) {
		t.Errorf("got:\n%s\nwant:\n%s\nall:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"), strings.Join(p.replayed(), "\n"))
	}
	var messages []string
	for _, n := range p.notes() {
		if m := n.Update.SessionMessage; m != nil && m.Content.Value != nil {
			messages = append(messages, string(n.SessionID)+" "+string(m.MessageID)+" "+(*m.Content.Value)[0].Text.Text)
		}
	}
	if want := []string{"agentA toolu_A count", "agentA toolu_M count again"}; !slices.Equal(messages, want) {
		t.Errorf("messages %q, want %q", messages, want)
	}

	before := len(p.notes())
	stopsWith(t, p.prompt(t, acp.SessionID(sid), "once more"), "end_turn")
	got = nil
	for _, l := range shape(p.notes()[before:], acp.SessionID(sid)) {
		if strings.HasPrefix(l, "agentA ") || strings.Contains(l, `"agentA"`) {
			got = append(got, l)
		}
	}
	want = []string{
		`s1 subagent {"sessionId":"agentA","state":{"state":"running"}}`,
		"agentA message toolu_N to agentA",
		"agentA call toolu_n1 Bash pending parent=toolu_N",
	}
	if !slices.Equal(got, want) {
		t.Errorf("after the load:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
