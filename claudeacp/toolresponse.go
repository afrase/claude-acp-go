package claudeacp

import (
	"context"
	"encoding/json"
	"strings"

	acp "github.com/afrase/acp-go/unstable"

	"github.com/afrase/claude-acp-go/claudeagent"
)

// postToolUse is the hook claude runs after each tool: it sends the
// tool's whole response under _meta.claudeCode.toolResponse, where
// clients read what the result's text doesn't say (a background
// command's task id, a subagent's id and model), and an edit's diff as
// claude made it.
func (s *session) postToolUse(ctx context.Context, in claudeagent.HookInput) (claudeagent.HookOutput, error) {
	s.caughtUp(ctx)
	switch {
	case in.AgentID != "":
	case isPlanTool("", in.ToolName): // the plan, not a call the client sees
		return claudeagent.HookOutput{}, nil
	case in.ToolName == "EnterPlanMode": // claude is in plan mode now
		s.cfg.modeChanged(claudeagent.PermissionPlan)
	}
	if json.Valid(in.ToolResponse) {
		s.tools.responded(in)
	}
	return claudeagent.HookOutput{}, nil
}

// responded is a tool's whole response, as the hook has it: it goes
// under _meta.claudeCode.toolResponse, and an edit's diff as claude made
// it replaces the one its input showed.
func (ts *tools) responded(in claudeagent.HookInput) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	r := report{response: in.ToolResponse}
	if in.ToolName == "Edit" || in.ToolName == "Write" {
		r.content, r.locations = patched(in.ToolResponse)
	}
	// claude ran the tool, so its whole input is on the way if the reader
	// hasn't taken it yet.
	switch t := ts.byID[in.ToolUseID]; {
	case t != nil && t.whole:
		ts.sendLocked(t, r)
	case len(ts.early) < maxEarly:
		ts.early[in.ToolUseID] = r
	}
}

// patched is an Edit's or a Write's change as diffs, from its response,
// and where each is, as claude-agent-acp gives them: one per hunk of its
// patch, the hunk's lines before and after, at its first line; a new
// file's whole text. A Write over a file with no hunks (nothing changed,
// or the file was too big to diff) is the whole old text and the new, or
// a line saying so: else the call would keep its input's diff, which
// shows a new file.
func patched(raw json.RawMessage) ([]acp.ToolCallContent, []acp.ToolCallLocation) {
	var r struct {
		Type         string  `json:"type"` // a Write's: "create" or "update"
		FilePath     string  `json:"filePath"`
		Content      *string `json:"content"`
		OriginalFile *string `json:"originalFile"`
		Patch        []struct {
			NewStart *uint32  `json:"newStart"`
			Lines    []string `json:"lines"`
		} `json:"structuredPatch"`
	}
	if json.Unmarshal(raw, &r) != nil || r.FilePath == "" {
		return nil, nil
	}
	if r.Type == "create" && r.Content != nil {
		return []acp.ToolCallContent{{Diff: &acp.Diff{Path: r.FilePath, NewText: *r.Content}}}, at(r.FilePath, nil)
	}
	var diffs []acp.ToolCallContent
	var locations []acp.ToolCallLocation
	for _, h := range r.Patch {
		var before, after []string
		for _, l := range h.Lines {
			switch {
			case l == noNewline:
			case strings.HasPrefix(l, "-"):
				before = append(before, l[1:])
			case strings.HasPrefix(l, "+"):
				after = append(after, l[1:])
			case l != "":
				before, after = append(before, l[1:]), append(after, l[1:])
			}
		}
		if before == nil && after == nil {
			continue
		}
		diffs = append(diffs, acp.ToolCallContent{Diff: &acp.Diff{Path: r.FilePath,
			OldText: strings.Join(before, "\n"), NewText: strings.Join(after, "\n")}})
		locations = append(locations, acp.ToolCallLocation{Path: r.FilePath, Line: h.NewStart})
	}
	if diffs == nil && r.Type == "update" && r.Content != nil {
		whole := textContent("Updated `" + r.FilePath + "` (previous content too large to diff)")
		if r.OriginalFile != nil {
			whole = acp.ToolCallContent{Diff: &acp.Diff{Path: r.FilePath, OldText: *r.OriginalFile, NewText: *r.Content}}
		}
		return []acp.ToolCallContent{whole}, at(r.FilePath, nil)
	}
	return diffs, locations
}

// noNewline is the line a patch's hunk has where its file doesn't end
// with a newline.
const noNewline = "\\ No newline at end of file"
