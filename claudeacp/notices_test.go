package claudeacp

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	acp "github.com/afrase/acp-go/unstable"

	"github.com/afrase/claude-acp-go/claudeagent/claudetest"
)

// chunks are the message chunks so far as text|messageId|_meta.
func (c *client) chunks() []string {
	var out []string
	for _, u := range c.of("agent_message_chunk") {
		ch := u.AgentMessageChunk
		meta, _ := json.Marshal(ch.Meta)
		out = append(out, ch.Content.Text.Text+"|"+string(ch.MessageID)+"|"+string(meta))
	}
	return out
}

// Claude's notices are id-less text with their level as a label and a
// mark in _meta, or ACP notices for a client that shows them (which isn't
// told what is only information); a model fallback is labelled text.
func TestNotices(t *testing.T) {
	steps := []claudetest.Step{user("id2"), echo("id2"),
		sendf(`{"type":"system","subtype":"informational","level":"info","content":"Synced."}`),
		sendf(`{"type":"system","subtype":"informational","level":"notice","content":"Opus 5.5's safeguards stopped the response above"}`),
		sendf(`{"type":"system","subtype":"model_refusal_fallback","original_model":"a","fallback_model":"b","content":"Retrying with b."}`),
		result(names("id2")), claudetest.Send(idle)}
	p := start(t, nil, fake(steps...))
	if got := stopReason(p.prompt(t, p.open(t), "go")); got != "end_turn" {
		t.Fatal(got)
	}
	want := []string{
		`Synced.||{"claudeCode":{"kind":"informational","level":"info"}}`,
		`**Notice:** Opus 5.5's safeguards stopped the response above||{"claudeCode":{"kind":"informational","level":"notice"}}`,
		`**Model fallback:** a declined this request; retried with b. The session will continue on b.||null`,
	}
	if got := p.chunks(); !slices.Equal(got, want) {
		t.Errorf("%q", got)
	}

	p = start(t, &acp.ClientCapabilities{Session: &acp.ClientSessionCapabilities{Notices: &acp.NoticeCapabilities{}}}, fake(steps...))
	if got := stopReason(p.prompt(t, p.open(t), "go")); got != "end_turn" {
		t.Fatal(got)
	}
	var got []string
	for _, u := range p.of("notice") {
		got = append(got, string(u.Notice.Severity)+" "+u.Notice.Title)
	}
	if want := []string{"info Opus 5.5's safeguards stopped the response above", "warning a declined this request; retried with b. The session will continue on b."}; !slices.Equal(got, want) || len(p.chunks()) != 0 {
		t.Errorf("%q %q", got, p.chunks())
	}
}

// A compaction is a "compact" tool call ending with its facts, and the
// context's use after it; one that fails says why. Recalled memories are
// a finished read.
func TestCompaction(t *testing.T) {
	steps := []claudetest.Step{user("id2"),
		sendf(`{"type":"system","subtype":"status","status":"compacting","uuid":"c1"}`),
		sendf(`{"type":"system","subtype":"compact_boundary","uuid":"b1","compact_metadata":{"trigger":"manual","pre_tokens":900,"post_tokens":80,"duration_ms":1200}}`),
		sendf(`{"type":"system","subtype":"status","status":null,"compact_result":"success"}`),
		sendf(`{"type":"system","subtype":"status","status":"compacting","uuid":"c2"}`),
		sendf(`{"type":"system","subtype":"status","status":null,"compact_result":"failed","compact_error":"too short"}`),
		sendf(`{"type":"system","subtype":"memory_recall","uuid":"m1","memories":[{"path":"a"},{"path":"b"}]}`),
		result(""), claudetest.Send(idle)}
	p := start(t, nil, fake(steps...))
	if got := stopReason(p.prompt(t, p.open(t), "/compact")); got != "end_turn" {
		t.Fatal(got)
	}
	want := []string{
		`tool_call {"toolCallId":"c1","title":"Compact conversation","name":"compact","kind":"think","status":"in_progress","_meta":{"claudeCode":{"toolName":"compact"}}}`,
		`tool_call_update {"toolCallId":"c1","status":"completed","rawOutput":{"trigger":"manual","preTokens":900,"postTokens":80,"durationMs":1200},"_meta":{"claudeCode":{"toolName":"compact"}}}`,
		`tool_call {"toolCallId":"c2","title":"Compact conversation","name":"compact","kind":"think","status":"in_progress","_meta":{"claudeCode":{"toolName":"compact"}}}`,
		`tool_call_update {"toolCallId":"c2","status":"failed","content":[{"type":"content","content":{"type":"text","text":"Compaction failed: too short"}}],"rawOutput":{"error":"too short"},"_meta":{"claudeCode":{"toolName":"compact"}}}`,
		`tool_call {"toolCallId":"m1","title":"Recalled 2 memories","name":"memory_recall","kind":"read","status":"completed","rawOutput":[{"path":"a"},{"path":"b"}],"_meta":{"claudeCode":{"toolName":"memory_recall"}}}`,
	}
	if got := p.toolUpdates(); !slices.Equal(got, want) {
		t.Errorf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if u := p.of("usage_update"); len(u) == 0 || u[0].UsageUpdate.Used != 80 {
		t.Errorf("%+v", u)
	}
}

// Claude's word that a compaction succeeded may come before its boundary,
// and twice: the boundary then adds its facts to the same call, and the
// second word is ignored.
func TestCompactionOutcomeFirst(t *testing.T) {
	success := sendf(`{"type":"system","subtype":"status","status":null,"compact_result":"success"}`)
	f := fake(user("id2"),
		sendf(`{"type":"system","subtype":"status","status":"compacting","uuid":"c1"}`),
		success,
		sendf(`{"type":"system","subtype":"compact_boundary","uuid":"b1","compact_metadata":{"trigger":"auto","pre_tokens":900,"post_tokens":80,"duration_ms":1200}}`),
		success,
		result(""), claudetest.Send(idle))
	p := start(t, nil, f)
	if got := stopReason(p.prompt(t, p.open(t), "/compact")); got != "end_turn" {
		t.Fatal(got)
	}
	want := []string{
		`tool_call {"toolCallId":"c1","title":"Compact conversation","name":"compact","kind":"think","status":"in_progress","_meta":{"claudeCode":{"toolName":"compact"}}}`,
		`tool_call_update {"toolCallId":"c1","status":"completed","_meta":{"claudeCode":{"toolName":"compact"}}}`,
		`tool_call_update {"toolCallId":"c1","rawOutput":{"trigger":"automatic","preTokens":900,"postTokens":80,"durationMs":1200},"_meta":{"claudeCode":{"toolName":"compact"}}}`,
	}
	if got := p.toolUpdates(); !slices.Equal(got, want) {
		t.Errorf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// /context's output comes twice, as claude's printed output and as its
// result's text: clients read the last.
func TestLocalCommand(t *testing.T) {
	f := fake(user("id2"),
		sendf(`{"type":"system","subtype":"local_command_output","uuid":"o1","content":"## Context Usage"}`),
		claudetest.Send(`{"type":"result","subtype":"success","uuid":"r1","result":"## Context Usage","total_cost_usd":0}`),
		claudetest.Send(idle))
	p := start(t, nil, f)
	if got := stopReason(p.prompt(t, p.open(t), "/context")); got != "end_turn" {
		t.Fatal(got)
	}
	if got := p.texts(); !slices.Equal(got, []string{"text:o1:## Context Usage", "text:r1:## Context Usage"}) {
		t.Errorf("%q", got)
	}
}

// A refused turn says why and stops refused; one claude couldn't run
// signed out fails asking for a sign-in, without claude's stand-in text.
func TestRefusalAndSignIn(t *testing.T) {
	f := fake(user("id2"), echo("id2"),
		event(`{"type":"message_start","message":{"id":"msg_1","model":"m"}}`),
		event(`{"type":"message_delta","delta":{"stop_reason":"refusal","stop_details":{"type":"refusal","explanation":"Not that."}}}`),
		result(names("id2")+`,"stop_reason":"refusal"`), claudetest.Send(idle),
		user("id3"), echo("id3"),
		claudetest.Send(`{"type":"assistant","message":{"id":"x","model":"<synthetic>","content":[{"type":"text","text":"Not logged in · Please run /login"}]}}`),
		claudetest.Send(`{"type":"result","subtype":"success","is_error":true,"result":"Not logged in · Please run /login","user_message_uuids":["id3"]}`),
		claudetest.Send(idle))
	p := start(t, nil, f)
	sid := p.open(t)
	if got := stopReason(p.prompt(t, sid, "a")); got != "refusal" {
		t.Error(got)
	}
	_, err := p.prompt(t, sid, "b")()
	if e, ok := errors.AsType[*acp.Error](err); !ok || e.Code != acp.ErrorCodeAuthenticationRequired {
		t.Errorf("%v", err)
	}
	if got := p.texts(); !slices.Equal(got, []string{"text::Not that."}) {
		t.Errorf("%q", got)
	}
}

// A compaction's facts are worded as claude-agent-acp words them: an
// automatic one's trigger is "automatic", and a count claude didn't give
// is left out; a boundary with no facts gives none.
func TestCompactionFacts(t *testing.T) {
	for _, tc := range []struct{ boundary, want string }{
		{`{"type":"system","subtype":"compact_boundary","uuid":"b1","compact_metadata":{"trigger":"auto","pre_tokens":900}}`,
			`tool_call_update {"toolCallId":"b1","status":"completed","rawOutput":{"trigger":"automatic","preTokens":900},"_meta":{"claudeCode":{"toolName":"compact"}}}`},
		{`{"type":"system","subtype":"compact_boundary","uuid":"b1"}`,
			`tool_call_update {"toolCallId":"b1","status":"completed","_meta":{"claudeCode":{"toolName":"compact"}}}`},
	} {
		p := start(t, nil, fake(user("id2"), sendf(tc.boundary), result(""), claudetest.Send(idle)))
		if got := stopReason(p.prompt(t, p.open(t), "go")); got != "end_turn" {
			t.Fatal(got)
		}
		if got := p.toolUpdates(); len(got) != 2 || got[1] != tc.want {
			t.Errorf("got\n%s\nwant\n%s", strings.Join(got, "\n"), tc.want)
		}
	}
}

// A failed /compact says why once, in its call: claude's copy of the
// error as a command's output and the result's text are not shown too.
func TestCompactionFailedOnce(t *testing.T) {
	f := fake(user("id2"),
		sendf(`{"type":"system","subtype":"status","status":"compacting","uuid":"c2"}`),
		sendf(`{"type":"system","subtype":"status","status":null,"compact_result":"failed","compact_error":"Not enough messages to compact."}`),
		sendf(`{"type":"system","subtype":"local_command_output","uuid":"o1","content":"Not enough messages to compact.\n"}`),
		sendf(`{"type":"system","subtype":"local_command_output","uuid":"o2","content":"Other output."}`),
		claudetest.Send(`{"type":"result","subtype":"success","uuid":"r1","result":"Not enough messages to compact.","total_cost_usd":0}`),
		claudetest.Send(idle))
	p := start(t, nil, f)
	if got := stopReason(p.prompt(t, p.open(t), "/compact")); got != "end_turn" {
		t.Fatal(got)
	}
	if got := p.texts(); !slices.Equal(got, []string{"text:o2:Other output."}) {
		t.Errorf("%q", got)
	}
	if got := p.toolUpdates(); len(got) != 2 || !strings.Contains(got[1], "Compaction failed: Not enough messages to compact.") {
		t.Errorf("%s", strings.Join(got, "\n"))
	}
}

// A client that declares ACP's compaction updates gets them, not a call:
// a completed compaction with the summary claude's hook gave, a failed
// one with why, and one still running at the turn's end cancelled.
func TestCompactionUpdates(t *testing.T) {
	f := fake(user("id2"),
		sendf(`{"type":"system","subtype":"status","status":"compacting","uuid":"c1"}`),
		sendf(`{"type":"control_request","request_id":"h1","request":{"subtype":"hook_callback","callback_id":"hook_0","input":{"hook_event_name":"PostCompact","trigger":"manual",`+
			`"compact_summary":"<analysis>\nThinking.\n</analysis>\n\n<summary>\nWe built X.\n</summary>"}}}`),
		claudetest.Expect(`{"type":"control_response","response":{"subtype":"success","request_id":"h1"}}`),
		sendf(`{"type":"system","subtype":"status","status":null,"compact_result":"success"}`),
		sendf(`{"type":"system","subtype":"compact_boundary","uuid":"b1","compact_metadata":{"trigger":"manual","pre_tokens":900,"post_tokens":80}}`),
		sendf(`{"type":"system","subtype":"status","status":"compacting","uuid":"c2"}`),
		sendf(`{"type":"system","subtype":"status","status":null,"compact_result":"failed","compact_error":"too short"}`),
		sendf(`{"type":"system","subtype":"status","status":"compacting","uuid":"c3"}`),
		result(""), claudetest.Send(idle))
	p := start(t, &acp.ClientCapabilities{Session: &acp.ClientSessionCapabilities{Compaction: &acp.CompactionCapabilities{}}}, f)
	if got := stopReason(p.prompt(t, p.open(t), "/compact")); got != "end_turn" {
		t.Fatal(got)
	}
	var got []string
	for _, u := range p.of("compaction_update") {
		b, _ := json.Marshal(u.CompactionUpdate)
		got = append(got, string(b))
	}
	want := []string{
		`{"compactionId":"c1","status":"in_progress"}`,
		`{"compactionId":"c1","status":"completed","summary":[{"type":"text","text":"We built X."}]}`,
		`{"compactionId":"c2","status":"in_progress"}`,
		`{"compactionId":"c2","status":"failed","error":"too short"}`,
		`{"compactionId":"c3","status":"in_progress"}`,
		`{"compactionId":"c3","status":"cancelled"}`,
	}
	if !slices.Equal(got, want) || len(p.toolUpdates()) != 0 {
		t.Errorf("got\n%s\nwant\n%s\n%q", strings.Join(got, "\n"), strings.Join(want, "\n"), p.toolUpdates())
	}
}

// claude's compaction output is shown as its summary only: without the
// model's analysis, or claude's framing of it for the model.
func TestCompactionSummary(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"<analysis>\nI think.\n</analysis>\n<summary>\nDone A.\n</summary>", "Done A."},
		{"<analysis>Mentions <summary> here.</analysis>\n<summary>\nDone B.\n</summary>", "Done B."},
		{"Lead <summary>Done C, quoting </summary> inline.</summary> after", "Done C, quoting </summary> inline."},
		{"This session is being continued from a previous conversation that ran out of context.\n\nSummary:\nDone D.\n\nIf you need specific details from before compaction, read the transcript.", "Done D."},
		{"This session is being continued from a previous conversation. Go on.", ""},
		{"Just a summary.", "Just a summary."},
	} {
		if got := compactionSummary(tc.in); got != tc.want {
			t.Errorf("%q: %q, want %q", tc.in, got, tc.want)
		}
	}
}

// A notice is plain text, its first line its title and the rest its
// description, said once per tool use; a model fallback's says what
// happened, and the API's explanation below, as claude-agent-acp says
// them.
func TestNoticeShape(t *testing.T) {
	f := fake(user("id2"), echo("id2"),
		sendf(`{"type":"system","subtype":"informational","level":"warning","tool_use_id":"tu1","content":"Waiting on MCP"}`),
		sendf(`{"type":"system","subtype":"informational","level":"warning","tool_use_id":"tu1","content":"Waiting on MCP"}`),
		sendf(`{"type":"system","subtype":"informational","level":"warning","content":"## Stop hook feedback:\n- [bash ./check.sh]: **tests failed** in `+"`go test`"+`\nline3"}`),
		sendf(`{"type":"system","subtype":"informational","level":"notice","content":"`+strings.Repeat("x", 300)+`\nmore"}`),
		sendf(`{"type":"system","subtype":"model_refusal_fallback","original_model":"a","fallback_model":"b","direction":"retry","scope":"local","api_refusal_category":"cyber","api_refusal_explanation":"Not that.","content":"Retrying."}`),
		result(names("id2")), claudetest.Send(idle))
	p := start(t, &acp.ClientCapabilities{Session: &acp.ClientSessionCapabilities{Notices: &acp.NoticeCapabilities{}}}, f)
	if got := stopReason(p.prompt(t, p.open(t), "go")); got != "end_turn" {
		t.Fatal(got)
	}
	var got []string
	for _, u := range p.of("notice") {
		got = append(got, string(u.Notice.Severity)+" "+u.Notice.Title+" | "+u.Notice.Description)
	}
	want := []string{
		"warning Waiting on MCP | ",
		"warning Stop hook feedback: | - [bash ./check.sh]: tests failed in go test\nline3",
		"info Claude reported a notice | " + strings.Repeat("x", 300) + "\nmore",
		"warning a declined this request (cyber); retried with b. Only that response came from b; the session stays on a. | Not that.",
	}
	if !slices.Equal(got, want) {
		t.Errorf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// Only Markdown that reads as such goes from a notice's text.
func TestPlainText(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"**bold** and __also__", "bold and also"},
		{"a ** b ** c", "a ** b ** c"},
		{"snake_case__name and 2**3", "snake_case__name and 2**3"},
		{"run `make` or ``x``, not a`b`c", "run make or ``x``, not a`b`c"},
		{"  ### Title\nbody #1", "Title\nbody #1"},
		{"**émoji ✓**", "émoji ✓"},
	} {
		if got := plainText(tc.in); got != tc.want {
			t.Errorf("%q: %q, want %q", tc.in, got, tc.want)
		}
	}
}

// A refused response's own explanation is the turn's, whether claude's
// banner comes after it or before; a banner for a turn no user started
// says nothing of the user's.
func TestRefusalExplanation(t *testing.T) {
	refused := event(`{"type":"message_delta","delta":{"stop_reason":"refusal","stop_details":{"type":"refusal","explanation":"Not that."}}}`)
	banner := sendf(`{"type":"system","subtype":"model_refusal_no_fallback","original_model":"m","api_refusal_explanation":null,"refused_user_message_uuid":"id2","content":"Claude Code is unable to respond to this request."}`)
	background := sendf(`{"type":"system","subtype":"model_refusal_no_fallback","original_model":"m","api_refusal_explanation":"Background.","refused_user_message_uuid":null,"content":"x"}`)
	for _, tc := range []struct {
		name  string
		steps []claudetest.Step
		want  string
	}{
		{"banner after", []claudetest.Step{refused, banner}, "Not that."},
		{"banner before", []claudetest.Step{banner, refused}, "Not that."},
		{"banner alone", []claudetest.Step{banner}, "Claude Code is unable to respond to this request."},
		{"not the user's", []claudetest.Step{background}, ""},
	} {
		steps := []claudetest.Step{user("id2"), echo("id2"), event(`{"type":"message_start","message":{"id":"msg_1","model":"m"}}`)}
		steps = append(steps, tc.steps...)
		steps = append(steps, result(names("id2")+`,"stop_reason":"refusal"`), claudetest.Send(idle))
		p := start(t, nil, fake(steps...))
		if got := stopReason(p.prompt(t, p.open(t), "a")); got != "refusal" {
			t.Error(tc.name, got)
		}
		var want []string
		if tc.want != "" {
			want = []string{"text::" + tc.want}
		}
		if got := p.texts(); !slices.Equal(got, want) {
			t.Errorf("%s: %q", tc.name, got)
		}
	}
}
