package claudeacp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/afrase/claude-acp-go/claudeagent"
	"github.com/afrase/claude-acp-go/claudeagent/claudetest"
)

// testdata/context/usage and usage-lists are claude's side of a recorded
// run (Claude Code 2.1.331, a fake Messages API), trimmed as shells/ has
// them. usage is a session asked for its context as it starts (its own
// ask, then "full" and claude's default), after a turn that wrote a
// CLAUDE.md and a project agent ("full", "summary"), and while a command
// of the next turn ran ("full"). usage-lists is a second session in the
// same folder, asked "full" and "summary", whose answers list that memory
// file and agent. The run's folder is written /tmp/claude/work.

// readCassette reads testdata's cassette at path.
func readCassette(t *testing.T, path ...string) *claudetest.Cassette {
	t.Helper()
	f, err := os.Open(filepath.Join(append([]string{"testdata"}, path...)...))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close() //nolint:errcheck // read only
	c, err := claudetest.ReadCassette(f)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// recordedUsage is a get_context_usage request and claude's answer to it,
// as recorded.
type recordedUsage struct {
	detail string
	answer json.RawMessage
}

// usageAnswers are c's get_context_usage requests with claude's answers,
// in the order they were asked.
func usageAnswers(t *testing.T, c *claudetest.Cassette) []recordedUsage {
	t.Helper()
	var out []recordedUsage
	asked := map[string]int{}
	for _, fr := range c.Frames {
		var f struct {
			Type      string `json:"type"`
			RequestID string `json:"request_id"`
			Request   struct {
				Subtype string `json:"subtype"`
				Detail  string `json:"detail"`
			} `json:"request"`
			Response struct {
				RequestID string          `json:"request_id"`
				Response  json.RawMessage `json:"response"`
			} `json:"response"`
		}
		if err := json.Unmarshal(fr.Frame, &f); err != nil {
			t.Fatal(err)
		}
		switch {
		case f.Type == "control_request" && f.Request.Subtype == "get_context_usage":
			asked[f.RequestID] = len(out)
			out = append(out, recordedUsage{detail: f.Request.Detail})
		case f.Type == "control_response":
			if i, ok := asked[f.Response.RequestID]; ok {
				out[i].answer = f.Response.Response
			}
		}
	}
	return out
}

// categoryIDs are u's categories' ids, in order.
func categoryIDs(u claudeagent.ContextUsage) []string {
	var out []string
	for _, c := range u.Categories {
		out = append(out, c.ID)
	}
	return out
}

// The context's usage, as the real claude answered it: passed through
// whole, decoded by claudeagent.ContextUsage, answered while a command
// of a turn runs. A "full" answer (and claude's default, which answers
// as "full" does) has a "messages" category; a "summary" one has none.
func TestContextUsageRecorded(t *testing.T) {
	c := readCassette(t, "context", "usage.cli.jsonl")
	recorded := usageAnswers(t, c)
	if len(recorded) != 6 || recorded[0].detail != "summary" {
		t.Fatalf("the cassette's asks: %+v", recorded)
	}
	fk := withTitle(claudetest.Replay(c), `null`)
	windowScripted.Store(fk, true) // its first ask is the session's own, as it starts
	p, sid := startOpen(t, nil, fk)
	p.permit = pick(optAllowOnce)

	next := 1
	ask := func() {
		t.Helper()
		want := recorded[next]
		next++
		match := `{"type":"control_request","request":{"subtype":"get_context_usage"}}`
		if want.detail != "" {
			match = `{"type":"control_request","request":{"subtype":"get_context_usage","detail":"` + want.detail + `"}}`
		}
		select {
		case <-fk.Awaiting(match):
		case <-time.After(guard):
			t.Fatalf("claude's replay never wanted the ask %q: %v", want.detail, fk.Err())
		}
		var got json.RawMessage
		if err := p.Call(t.Context(), ContextUsageMethod, ContextUsageRequest{SessionID: string(sid), Detail: want.detail}, &got); err != nil {
			t.Fatal(err)
		}
		if keysSorted(got) != keysSorted(want.answer) {
			t.Errorf("detail %q: not claude's answer, whole: %.300s", want.detail, got)
		}
		var u claudeagent.ContextUsage
		if err := json.Unmarshal(got, &u); err != nil {
			t.Fatal(err)
		}
		hasMessages := slices.Contains(categoryIDs(u), "messages")
		if u.RawMaxTokens != 1_000_000 || u.Model != "claude-opus-5-5" || len(u.Categories) == 0 || hasMessages == (want.detail == "summary") {
			t.Errorf("detail %q decoded: %d %q %v", want.detail, u.RawMaxTokens, u.Model, categoryIDs(u))
		}
		for _, cat := range u.Categories {
			if cat.ID == "" || cat.Kind == "" || cat.Name == "" {
				t.Errorf("detail %q: a category without id, kind or name: %+v", want.detail, cat)
			}
		}
	}
	prompt := func(text string) func() {
		t.Helper()
		select {
		case <-fk.Awaiting(`{"type":"user"}`):
		case <-time.After(guard):
			t.Fatalf("%q: claude's replay never wanted it: %v", text, fk.Err())
		}
		w := p.prompt(t, sid, text)
		return func() { t.Helper(); stopsWith(t, w, "end_turn") }
	}

	ask() // full, new
	ask() // claude's default
	prompt("write the notes")()
	ask() // full, after a turn
	ask() // summary
	done := prompt("wait a little")
	ask() // full, while the turn's command runs
	done()
	<-fk.Played()
	if err := fk.Err(); err != nil {
		t.Fatal(err)
	}

	// That command ran in the foreground, but for long enough that claude
	// made it a task (is_backgrounded false): its call got the task's
	// updates before its result.
	var statuses []string
	for _, n := range p.notes() {
		if u := n.Update.ToolCallUpdate; u != nil && u.ToolCallID == "toolu_wait" {
			if task, ok := claudeMeta(u.Meta)["task"].(map[string]any); ok {
				statuses = append(statuses, str(task["status"]))
			} else if u.Status != "" {
				statuses = append(statuses, "call "+string(u.Status))
			}
		}
	}
	sameLines(t, statuses, []string{"running", "completed", "call completed"})
}

// A "full" answer lists the memory files, agents, MCP tools and skills
// claude loaded, in claude's own shape: each row as the real claude sent
// it, which claudeagent.ContextUsage leaves to Raw. A "summary" answer
// lists them too. An MCP server's tools are listed once it has
// connected: the first answer had none yet.
func TestContextUsageLists(t *testing.T) {
	recorded := usageAnswers(t, readCassette(t, "context", "usage-lists.cli.jsonl"))
	if len(recorded) != 2 || recorded[0].detail != "full" || recorded[1].detail != "summary" {
		t.Fatalf("the cassette's asks: %+v", recorded)
	}
	seen := map[string]bool{} // the lists either answer had rows in
	for _, r := range recorded {
		var lists struct {
			MemoryFiles []map[string]any `json:"memoryFiles"`
			Agents      []map[string]any `json:"agents"`
			MCPTools    []map[string]any `json:"mcpTools"`
			Skills      struct {
				SkillFrontmatter []map[string]any `json:"skillFrontmatter"`
			} `json:"skills"`
		}
		if err := json.Unmarshal(r.answer, &lists); err != nil {
			t.Fatal(err)
		}
		keys := func(rows []map[string]any) []string {
			if len(rows) == 0 {
				return nil
			}
			var out []string
			for k := range rows[0] {
				out = append(out, k)
			}
			slices.Sort(out)
			return out
		}
		for _, tc := range []struct {
			what string
			rows []map[string]any
			want []string
		}{
			{"memoryFiles", lists.MemoryFiles, []string{"path", "tokens", "type"}},
			{"agents", lists.Agents, []string{"agentType", "path", "source", "tokens"}},
			{"mcpTools", lists.MCPTools, []string{"isLoaded", "name", "serverName", "tokens"}},
			{"skills.skillFrontmatter", lists.Skills.SkillFrontmatter, []string{"name", "source", "tokens"}},
		} {
			got := keys(tc.rows)
			if got != nil {
				seen[tc.what] = true
			}
			if got != nil && !slices.Equal(got, tc.want) {
				t.Errorf("%s %s: a row's keys %v, want %v", r.detail, tc.what, got, tc.want)
			}
		}
		if m, a := lists.MemoryFiles[0], lists.Agents[0]; m["path"] != "/tmp/claude/work/CLAUDE.md" || m["type"] != "Project" ||
			a["agentType"] != "helper" || a["source"] != "projectSettings" {
			t.Errorf("%s: %v %v", r.detail, m, a)
		}
		var u claudeagent.ContextUsage
		if err := json.Unmarshal(r.answer, &u); err != nil {
			t.Fatal(err)
		}
		if ids := categoryIDs(u); !slices.Contains(ids, "memory_files") || !slices.Contains(ids, "custom_agents") {
			t.Errorf("%s: categories %v", r.detail, ids)
		}
	}
	if len(seen) != 4 {
		t.Errorf("rows in %v only", seen)
	}
}
