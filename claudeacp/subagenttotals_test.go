package claudeacp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	acp "github.com/afrase/acp-go/unstable"

	"github.com/afrase/claude-acp-go/claudeagent/claudetest"
	"github.com/afrase/claude-acp-go/claudeagent/sessions"
)

// totalsLines are the updates in notes that carry a toolResponse of a
// subagent's totals: "<session> <call> <_meta>", keys sorted.
func totalsLines(notes []acp.SessionNotification) []string {
	var out []string
	for _, n := range notes {
		u := n.Update.ToolCallUpdate
		if u == nil {
			continue
		}
		resp, _ := claudeMeta(u.Meta)["toolResponse"].(map[string]any)
		if _, ok := resp["totalTokens"]; !ok {
			continue
		}
		b, _ := json.Marshal(u.Meta)
		out = append(out, string(n.SessionID)+" "+string(u.ToolCallID)+" "+keysSorted(b))
	}
	return out
}

// totalsMeta is the _meta of an Agent call's update with a subagent's
// totals, keys sorted.
func totalsMeta(parent, response string) string {
	p := ""
	if parent != "" {
		p = `"parentToolUseId":"` + parent + `",`
	}
	return keysSorted([]byte(`{"claudeCode":{` + p + `"toolName":"Agent","toolResponse":` + response + `}}`))
}

// launchedResult is toolu_p's result as claude gives it for a subagent it
// runs in the background: the launch, at once.
var launchedResult = sendf(`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_p",` +
	`"content":"Async agent launched successfully."}]},"tool_use_result":{"isAsync":true,"status":"async_launched","agentId":"a1"}}`)

// A background subagent's totals, which claude gives only in its task
// notification (usage), go on the Agent call that started it as a foreground
// subagent's do: _meta.claudeCode.toolResponse's totalTokens,
// totalToolUseCount and totalDurationMs, every client alike, once. A
// call whose result wasn't a launch in the background (a foreground
// subagent's, whose own response has its totals) gets none.
func TestBackgroundSubagentTotals(t *testing.T) {
	end := `{"type":"system","subtype":"task_notification","task_id":"a1","tool_use_id":"toolu_p","status":"completed",` +
		`"output_file":"/tmp/claude/tasks/a1.output","summary":"Agent \"Count\" completed",` +
		`"usage":{"total_tokens":182000,"tool_uses":39,"duration_ms":475000}}`
	want := totalsMeta("", `{"totalTokens":182000,"totalToolUseCount":39,"totalDurationMs":475000}`)
	for _, tc := range []struct {
		name   string
		caps   *acp.ClientCapabilities
		launch claudetest.Step
		want   []string
	}{
		{"background", nil, launchedResult, []string{"toolu_p " + want}},
		{"background, subagents as sessions", withSubagents, launchedResult, []string{"toolu_p " + want}},
		{"foreground", nil, agentDone, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := fake(user("id2"), echo("id2"), agentCall(true), taskStarted(`,"is_backgrounded":true`), tc.launch,
				sendf(end), sendf(end), result(names("id2")), claudetest.Send(idle))
			p, sid := startOpen(t, tc.caps, f)
			stopsWith(t, p.prompt(t, sid, "go"), "end_turn")
			var want []string
			for _, w := range tc.want {
				want = append(want, string(sid)+" "+w)
			}
			if got := totalsLines(p.notes()); !slices.Equal(got, want) {
				t.Errorf("got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
			}
		})
	}
}

// A notification with no usage sends no totals.
func TestBackgroundSubagentNoUsage(t *testing.T) {
	f := fake(user("id2"), echo("id2"), agentCall(true), taskStarted(`,"is_backgrounded":true`), launchedResult,
		sendf(`{"type":"system","subtype":"task_notification","task_id":"a1","tool_use_id":"toolu_p","status":"stopped"}`),
		result(names("id2")), claudetest.Send(idle))
	p, sid := startOpen(t, nil, f)
	stopsWith(t, p.prompt(t, sid, "go"), "end_turn")
	if got := totalsLines(p.notes()); got != nil {
		t.Errorf("totals: %q", got)
	}
}

// On claude's own transcripts (a recorded run of nested subagents, both
// in the background), a load replays each subagent's totals from the
// <usage> of claude's notification of its end, on the Agent call that
// started it: the outer one's on the session, the inner one's on the
// outer one's session, naming its stream.
func TestLoadRecordedSubagentTotals(t *testing.T) {
	history := filepath.Join("testdata", "nested")
	p := start(t, withSubagents, fake())
	cwd := t.TempDir()
	project := sessions.Store{ConfigDir: p.agent.cfg.ConfigDir}.ProjectDir(cwd)
	if err := os.MkdirAll(filepath.Dir(project), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.CopyFS(project, os.DirFS(history)); err != nil {
		t.Fatal(err)
	}
	sid := "ea46184f-9ed6-43ab-9d34-7946b3cc2ceb"
	p.mustLoad(t, sid, cwd)
	want := []string{
		"ad7ceac0c2765981b toolu_fake_0004 " + totalsMeta("toolu_fake_0002", `{"totalTokens":6976,"totalToolUseCount":0,"totalDurationMs":57}`),
		sid + " toolu_fake_0002 " + totalsMeta("", `{"totalTokens":8152,"totalToolUseCount":2,"totalDurationMs":3175}`),
	}
	if got := totalsLines(p.notes()); !slices.Equal(got, want) {
		t.Errorf("got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// A replayed notification's <usage> reads as the totals; one without it,
// or without the tokens, reads as none.
func TestTaskEndsUsage(t *testing.T) {
	note := func(usage string) string {
		return "<task-notification>\n<task-id>a1</task-id>\n<tool-use-id>toolu_p</tool-use-id>\n<status>completed</status>\n" +
			usage + "\n</task-notification>"
	}
	for _, tc := range []struct {
		usage string
		want  *totals
	}{
		{"<usage><subagent_tokens>8152</subagent_tokens><tool_uses>2</tool_uses><duration_ms>3175</duration_ms></usage>",
			&totals{TotalTokens: 8152, TotalToolUseCount: 2, TotalDurationMs: 3175}},
		{"<usage><subagent_tokens>10</subagent_tokens></usage>", &totals{TotalTokens: 10}},
		{"<usage><tool_uses>2</tool_uses></usage>", nil},
		{"", nil},
	} {
		ends := taskEnds(note(tc.usage))
		if len(ends) != 1 || ends[0].call != "toolu_p" {
			t.Fatalf("%q: %+v", tc.usage, ends)
		}
		if got := ends[0].usage; (got == nil) != (tc.want == nil) || got != nil && *got != *tc.want {
			t.Errorf("%q: got %+v, want %+v", tc.usage, got, tc.want)
		}
	}
}
