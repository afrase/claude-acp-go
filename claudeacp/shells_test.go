package claudeacp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	acp "github.com/afrase/acp-go/unstable"

	"github.com/afrase/claude-acp-go/claudeagent/claudetest"
)

// testdata/shells are claude's side of three recorded runs (Claude Code
// 2.1.312, a fake Messages API), trimmed to what the agent reads: a
// background command that ends (completed), one stopped by claude's
// TaskStop (taskstop), and one whose process group was killed from
// outside (killed). Their output files' folder is written /tmp/claude.

// shellLines are what a client goes by in notes, a line each: the calls
// and their statuses, text (a run of chunks as one), usage by origin, a
// call's toolResponse, and each update that carries a background
// command's task, whole, as the client receives it.
func shellLines(t *testing.T, notes []acp.SessionNotification) (lines []string) {
	t.Helper()
	for _, n := range notes {
		u := n.Update
		var line string
		switch {
		case u.AgentMessageChunk != nil:
			line = "text"
			if len(lines) > 0 && lines[len(lines)-1] == line {
				continue
			}
		case u.UsageUpdate != nil:
			origin, _ := u.UsageUpdate.Meta["_claude/origin"].(map[string]any)
			line = "usage " + str(origin["kind"])
		case u.ToolCall != nil:
			line = "call " + string(u.ToolCall.ToolCallID) + " " + string(u.ToolCall.Status)
		case u.ToolCallUpdate != nil && claudeMeta(u.ToolCallUpdate.Meta)["task"] != nil:
			b, err := json.Marshal(u)
			if err != nil {
				t.Fatal(err)
			}
			line = keysSorted(b)
		case u.ToolCallUpdate != nil && claudeMeta(u.ToolCallUpdate.Meta)["toolResponse"] != nil:
			line = "response " + string(u.ToolCallUpdate.ToolCallID)
		case u.ToolCallUpdate != nil && u.ToolCallUpdate.Status != "":
			line = "update " + string(u.ToolCallUpdate.ToolCallID) + " " + string(u.ToolCallUpdate.Status)
		default:
			continue
		}
		lines = append(lines, line)
	}
	return lines
}

// shellUpdate is the update of the call toolu_fake_0002 that carries
// task, as the client receives it.
func shellUpdate(task string) string {
	return keysSorted([]byte(`{"sessionUpdate":"tool_call_update","toolCallId":"toolu_fake_0002","_meta":{"claudeCode":{"toolName":"Bash","task":` + task + `}}}`))
}

// keysSorted is the JSON b with its objects' keys in order, so that two
// encodings of the same value compare equal.
func keysSorted(b []byte) string {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return "not JSON: " + string(b)
	}
	b, _ = json.Marshal(v)
	return string(b)
}

// A background command's task goes to the client on the Bash call that
// started it, every client alike: running as claude starts it, then each
// change claude tells of its end, in the order claude tells it among
// the turn's updates: the status and when it ended (task_updated), then
// claude's last word with the output file and its summary
// (task_notification). The call itself stays completed, as its result
// left it.
func TestShellTasks(t *testing.T) {
	for _, tc := range []struct {
		name    string
		prompts []string
		want    []string
	}{
		{"completed", []string{"run it in the background"}, []string{
			"call toolu_fake_0002 pending",
			shellUpdate(`{"id":"bqphuo3vw","type":"local_bash","status":"running"}`),
			"response toolu_fake_0002",
			"update toolu_fake_0002 completed",
			"text",
			"usage human",
			shellUpdate(`{"id":"bqphuo3vw","type":"local_bash","status":"completed","endTime":1793314896880}`),
			shellUpdate(`{"id":"bqphuo3vw","type":"local_bash","status":"completed","outputFile":"/tmp/claude/tasks/bqphuo3vw.output",` +
				`"summary":"Background command \"Finish soon\" completed (exit code 0)","endTime":1793314896880}`),
			"text",
			"usage task-notification",
		}},
		{"taskstop", []string{"run it in the background", "stop it"}, []string{
			"call toolu_fake_0002 pending",
			shellUpdate(`{"id":"bw5r8w073","type":"local_bash","status":"running"}`),
			"response toolu_fake_0002",
			"update toolu_fake_0002 completed",
			"text",
			"usage human",
			"call toolu_fake_0008 pending",
			shellUpdate(`{"id":"bw5r8w073","type":"local_bash","status":"killed","endTime":1793314898564}`),
			shellUpdate(`{"id":"bw5r8w073","type":"local_bash","status":"stopped","outputFile":"/tmp/claude/tasks/bw5r8w073.output",` +
				`"summary":"Wait long","endTime":1793314898564}`),
			"response toolu_fake_0008",
			"update toolu_fake_0008 completed",
			"text",
			"usage human",
		}},
		{"killed", []string{"run it in the background"}, []string{
			"call toolu_fake_0002 pending",
			shellUpdate(`{"id":"b709r21cm","type":"local_bash","status":"running"}`),
			"response toolu_fake_0002",
			"update toolu_fake_0002 completed",
			"text",
			"usage human",
			shellUpdate(`{"id":"b709r21cm","type":"local_bash","status":"failed","endTime":1793314897888}`),
			shellUpdate(`{"id":"b709r21cm","type":"local_bash","status":"failed","outputFile":"/tmp/claude/tasks/b709r21cm.output",` +
				`"summary":"Background command \"Wait long\" failed with exit code 144","endTime":1793314897888}`),
			"text",
			"usage task-notification",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, err := os.Open(filepath.Join("testdata", "shells", tc.name+".cli.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			c, err := claudetest.ReadCassette(f)
			_ = f.Close()
			if err != nil {
				t.Fatal(err)
			}
			fk := withTitle(claudetest.Replay(c), `null`)
			p := start(t, nil, fk)
			sid := p.open(t)
			for _, prompt := range tc.prompts {
				select {
				case <-fk.Awaiting(`{"type":"user"}`):
				case <-time.After(guard):
					t.Fatalf("%q: claude's replay never wanted it: %v", prompt, fk.Err())
				}
				if got := stopReason(p.prompt(t, sid, prompt)); got != "end_turn" {
					t.Fatalf("%q: %s", prompt, got)
				}
			}
			<-fk.Played()
			if err := fk.Err(); err != nil {
				t.Fatal(err)
			}
			eventually(t, "the last usage", func() bool {
				got := shellLines(t, p.notes())
				return len(got) > 0 && got[len(got)-1] == tc.want[len(tc.want)-1] && len(got) >= len(tc.want)
			})
			if got := shellLines(t, p.notes()); !slices.Equal(got, tc.want) {
				t.Errorf("got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(tc.want, "\n"))
			}
		})
	}
}

// A subagent's task is no background command: its start, its end
// (killed, with when, then stopped) give what they gave before, and no
// update carries a task.
func TestShellTasksNotSubagents(t *testing.T) {
	f := fake(user("id2"), echo("id2"), agentCall(true), taskStarted(""), agentDone,
		sendf(`{"type":"system","subtype":"task_updated","task_id":"a1","patch":{"status":"killed","end_time":1793314898564}}`),
		sendf(`{"type":"system","subtype":"task_notification","task_id":"a1","tool_use_id":"toolu_p","status":"stopped","output_file":"/tmp/claude/tasks/a1.output","summary":"Count"}`),
		result(names("id2")), claudetest.Send(idle))
	p := start(t, withSubagents, f)
	sid := p.open(t)
	stopsWith(t, p.prompt(t, sid, "go"), "end_turn")
	want := []string{
		"s1 call toolu_p Agent pending",
		`s1 subagent {"sessionId":"a1","title":"counter (general-purpose)","description":"Count","capabilities":{"cancel":{}},"state":{"state":"running"}}`,
		"a1 message toolu_p to a1",
		"s1 update toolu_p completed",
		`s1 subagent {"sessionId":"a1","state":{"state":"idle","stopReason":"cancelled"}}`,
		"s1 usage human",
	}
	if got := shape(p.notes(), sid); !slices.Equal(got, want) {
		t.Errorf("got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	for _, n := range p.notes() {
		if u := n.Update.ToolCallUpdate; u != nil && claudeMeta(u.Meta)["task"] != nil {
			t.Errorf("a task on %s", u.ToolCallID)
		}
	}
}

// A subagent's background command is its Bash call's, on the subagent's
// session and naming its Agent call, as the call's other updates are;
// it outlives the subagent.
func TestShellTaskOfSubagent(t *testing.T) {
	f := fake(user("id2"), echo("id2"), agentCall(true), taskStarted(""), agentDone,
		sendf(`{"type":"assistant","parent_tool_use_id":"toolu_p","message":{"id":"msg_s","content":[{"type":"tool_use","id":"toolu_1","name":"Bash",`+
			`"input":{"command":"sleep 9","run_in_background":true}}]}}`),
		sendf(`{"type":"system","subtype":"task_started","task_id":"b1","tool_use_id":"toolu_1","description":"Sleep","task_type":"local_bash","is_backgrounded":true}`),
		sendf(`{"type":"user","parent_tool_use_id":"toolu_p","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"Command running in background with ID: b1."}]}}`),
		taskEnd("completed"),
		sendf(`{"type":"system","subtype":"task_updated","task_id":"b1","patch":{"status":"completed","end_time":7}}`),
		sendf(`{"type":"system","subtype":"task_notification","task_id":"b1","tool_use_id":"toolu_1","status":"completed","output_file":"/tmp/claude/tasks/b1.output"}`),
		result(names("id2")), claudetest.Send(idle))
	p := start(t, withSubagents, f)
	sid := p.open(t)
	stopsWith(t, p.prompt(t, sid, "go"), "end_turn")
	var got []string
	for _, n := range p.notes() {
		if u := n.Update.ToolCallUpdate; u != nil && claudeMeta(u.Meta)["task"] != nil {
			b, _ := json.Marshal(u.Meta)
			got = append(got, string(n.SessionID)+" "+string(u.ToolCallID)+" "+keysSorted(b))
		}
	}
	want := []string{
		`a1 toolu_1 {"claudeCode":{"parentToolUseId":"toolu_p","task":{"id":"b1","status":"running","type":"local_bash"},"toolName":"Bash"}}`,
		`a1 toolu_1 {"claudeCode":{"parentToolUseId":"toolu_p","task":{"endTime":7,"id":"b1","status":"completed","type":"local_bash"},"toolName":"Bash"}}`,
		`a1 toolu_1 {"claudeCode":{"parentToolUseId":"toolu_p","task":{"endTime":7,"id":"b1","outputFile":"/tmp/claude/tasks/b1.output","status":"completed","type":"local_bash"},"toolName":"Bash"}}`,
	}
	if !slices.Equal(got, want) {
		t.Errorf("got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// A load replays a background command's end where the history has it,
// once: claude's notification of it (kept as a user message, and as its
// queue's record), or claude's TaskStop of it. A command the history
// says nothing more of replays no task: nothing says it still runs.
func TestLoadReplaysShellEnds(t *testing.T) {
	p := start(t, nil, fake())
	cwd, sid := t.TempDir(), nthUUID(1)
	bash := func(call, task string) []rec {
		return []rec{
			asstRec("a"+call, "msg_"+call, useBlock("toolu_"+call, "Bash", `{"command":"sleep 9","run_in_background":true}`)),
			userRec("r"+call, resultBlocks("toolu_"+call, "Command running in background with ID: "+task+".", false),
				"toolUseResult", map[string]any{"stdout": "", "backgroundTaskId": task}),
		}
	}
	note := func(task, status, summary string) string {
		return "<task-notification>\n<task-id>" + task + "</task-id>\n<tool-use-id>toolu_x</tool-use-id>\n<output-file>/tmp/claude/tasks/" +
			task + ".output</output-file>\n<status>" + status + "</status>\n<summary>" + summary + "</summary>\n</task-notification>"
	}
	records := []rec{userRec("u1", "Run them")}
	for _, c := range []struct{ call, task string }{{"A", "bA"}, {"B", "bB"}, {"C", "bC"}, {"D", "bD"}} {
		records = append(records, bash(c.call, c.task)...)
	}
	records = append(records,
		asstRec("t1", "msg_1", textBlock("They run.")),
		queueOp("enqueue", note("bA", "completed", `Background command "A" completed (exit code 0)`)),
		userRec("n1", note("bA", "completed", `Background command "A" completed (exit code 0)`)),
		userRec("n2", note("bB", "failed", `Background command "B" failed with exit code 144`)),
		asstRec("s1", "msg_2", useBlock("toolu_S", "TaskStop", `{"task_id":"bC"}`)),
		userRec("s2", resultBlocks("toolu_S", "Successfully stopped task: bC", false),
			"toolUseResult", map[string]any{"task_id": "bC", "task_type": "local_bash"}),
	)
	p.writeHistory(t, cwd, sid, records...)
	if _, err := p.load(sid, cwd); err != nil {
		t.Fatal(err)
	}
	task := func(call, task string) string {
		return sid + " task toolu_" + call + " " + keysSorted([]byte(`{"claudeCode":{"toolName":"Bash","task":`+task+`}}`))
	}
	want := []string{
		sid + " user u1 Run them",
		sid + " call toolu_A sleep 9 pending",
		sid + " update toolu_A completed",
		sid + " call toolu_B sleep 9 pending",
		sid + " update toolu_B completed",
		sid + " call toolu_C sleep 9 pending",
		sid + " update toolu_C completed",
		sid + " call toolu_D sleep 9 pending",
		sid + " update toolu_D completed",
		sid + " text msg_1 They run.",
		task("A", `{"id":"bA","type":"local_bash","status":"completed","outputFile":"/tmp/claude/tasks/bA.output","summary":"Background command \"A\" completed (exit code 0)"}`),
		task("B", `{"id":"bB","type":"local_bash","status":"failed","outputFile":"/tmp/claude/tasks/bB.output","summary":"Background command \"B\" failed with exit code 144"}`),
		sid + " call toolu_S TaskStop pending",
		sid + " update toolu_S completed",
		task("C", `{"id":"bC","type":"local_bash","status":"stopped"}`),
		sid + " commands",
	}
	got := p.replayed()
	for i, n := range p.notes() {
		if u := n.Update.ToolCallUpdate; u != nil && claudeMeta(u.Meta)["task"] != nil {
			b, _ := json.Marshal(u.Meta)
			got[i] = string(n.SessionID) + " task " + string(u.ToolCallID) + " " + keysSorted(b)
		}
	}
	if !slices.Equal(got, want) {
		t.Errorf("got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// A load of a session that is open already replays a background command
// its claude still runs as running, which the history can't tell.
func TestLoadOpenSessionShell(t *testing.T) {
	f := fake(user("id1"), echo("id1"),
		assistant(`{"type":"tool_use","id":"toolu_A","name":"Bash","input":{"command":"sleep 9","run_in_background":true}}`),
		sendf(`{"type":"system","subtype":"task_started","task_id":"bA","tool_use_id":"toolu_A","task_type":"local_bash","is_backgrounded":true}`),
		sendf(`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_A","content":"Command running in background with ID: bA."}]},`+
			`"tool_use_result":{"backgroundTaskId":"bA"}}`),
		result(names("id1")), claudetest.Send(idle))
	p := start(t, nil, f)
	cwd, sid := t.TempDir(), nthUUID(1)
	p.writeHistory(t, cwd, sid, userRec("u1", "Hi"))
	if _, err := p.load(sid, cwd); err != nil {
		t.Fatal(err)
	}
	stopsWith(t, p.prompt(t, acp.SessionID(sid), "go"), "end_turn")
	// claude has written the turn to the transcript.
	p.writeHistory(t, cwd, sid, userRec("u1", "Hi"), userRec("u2", "go"),
		asstRec("a2", "msg_2", useBlock("toolu_A", "Bash", `{"command":"sleep 9","run_in_background":true}`)),
		userRec("r2", resultBlocks("toolu_A", "Command running in background with ID: bA.", false), "toolUseResult", map[string]any{"backgroundTaskId": "bA"}))
	before := len(p.notes())
	if _, err := p.load(sid, cwd); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, n := range p.notes()[before:] {
		if u := n.Update.ToolCallUpdate; u != nil && claudeMeta(u.Meta)["task"] != nil {
			b, _ := json.Marshal(u.Meta)
			got = append(got, string(u.ToolCallID)+" "+keysSorted(b))
		}
	}
	want := []string{`toolu_A {"claudeCode":{"task":{"id":"bA","status":"running","type":"local_bash"},"toolName":"Bash"}}`}
	if !slices.Equal(got, want) {
		t.Errorf("got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
