package claudeacp

import (
	"cmp"
	"context"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	acp "github.com/afrase/acp-go/unstable"

	"github.com/afrase/claude-acp-go/claudeagent"
)

// What claude says of its own, beside the model's words: notices, a
// compaction, recalled memories, a slash command's output, a refusal.

// notice tells the user something of claude's: as ACP's notice to a
// client that shows them; else as message text with no message id,
// "**Level:** " before it (none for info) and _meta.claudeCode saying it
// is a notice, so a client that knows claude can tell it from the
// model's words (the shape claude-agent-acp gives, which clients read).
func (s *session) notice(level, content string) {
	if content == "" {
		return
	}
	if s.a.clientCaps().notices {
		if level == "info" {
			return // a notice client is told what matters only
		}
		sev := acp.NoticeSeverityInfo
		if level == "warning" {
			sev = acp.NoticeSeverityWarning
		}
		s.update(acp.SessionUpdate{SessionUpdate: "notice", Notice: &acp.Notice{Severity: sev, Title: content}})
		return
	}
	text := content
	if level != "" && level != "info" {
		text = "**" + strings.ToUpper(level[:1]) + level[1:] + ":** " + content
	}
	s.update(acp.SessionUpdate{SessionUpdate: "agent_message_chunk", AgentMessageChunk: &acp.ContentChunk{
		Content: acp.ContentBlock{Text: &acp.TextContent{Text: text}},
		Meta:    claudeCodeMeta{Kind: "informational", Level: &level}.meta(),
	}})
}

// modelFallback is claude retrying a refused response on another model:
// text with no message id, as claude-agent-acp sends it, or a warning.
func (s *session) modelFallback(m *claudeagent.ModelRefusalFallback) {
	text := m.Content
	if text == "" {
		text = "Retried with " + m.FallbackModel + " after " + m.OriginalModel + " refused."
	}
	if s.a.clientCaps().notices {
		s.update(acp.SessionUpdate{SessionUpdate: "notice", Notice: &acp.Notice{Severity: acp.NoticeSeverityWarning, Title: text}})
		return
	}
	s.update(acp.SessionUpdate{SessionUpdate: "agent_message_chunk", AgentMessageChunk: &acp.ContentChunk{
		Content: acp.ContentBlock{Text: &acp.TextContent{Text: "**Model fallback:** " + text}},
	}})
}

// compacting starts a compaction: as ACP's compaction updates to a
// client that declared them, else as a tool call of its own ("compact",
// as claude-agent-acp names it). One that has ended is kept until the
// turn's result, so its boundary and claude's word on its outcome end the
// same call in either order; a start after it is a new one.
func (ts *tools) compacting(id string) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.compactingLocked(id)
}

func (ts *tools) compactingLocked(id string) *tool {
	if ts.compact != nil && !ts.compact.ended {
		return ts.compact
	}
	if id == "" {
		id = "compact-" + ts.newID()
	}
	t := &tool{id: id, name: "compact"}
	ts.compact, ts.compactShown = t, true
	if ts.compactUpdates {
		ts.compactionLocked(t, acp.CompactionStatusInProgress, "")
		return t
	}
	ts.sendLocked(t, report{status: acp.ToolCallStatusInProgress, title: "Compact conversation", kind: acp.ToolKindThink})
	return t
}

// compactionLocked sends an ACP compaction update of the compaction t: a
// completed one carries the summary claude's hook gave, a failed one why.
func (ts *tools) compactionLocked(t *tool, status acp.CompactionStatus, why string) {
	u := &acp.CompactionUpdate{CompactionID: acp.CompactionID(t.id), Status: status, Error: why}
	if status == acp.CompactionStatusCompleted && ts.summary != "" {
		u.Summary = []acp.ContentBlock{{Text: &acp.TextContent{Text: ts.summary}}}
		ts.summary = ""
	}
	ts.out.emit("", acp.SessionUpdate{SessionUpdate: "compaction_update", CompactionUpdate: u})
}

// compacted ends the compaction at its boundary, with its facts; one
// claude has already said the outcome of gets only the facts.
func (ts *tools) compacted(m *claudeagent.CompactBoundary) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	t := ts.compact
	if t == nil {
		t = ts.compactingLocked(m.UUID) // an automatic one may come without its start
	}
	first := !t.ended
	t.ended = true
	if ts.compactUpdates {
		if first { // the facts aren't for ACP's update
			ts.compactionLocked(t, acp.CompactionStatusCompleted, "")
		}
		return
	}
	r := report{status: acp.ToolCallStatusCompleted}
	if !first {
		r.status = ""
	}
	if c := m.CompactMetadata; c != nil {
		// The facts as claude-agent-acp words them: a count claude didn't
		// give is left out, not 0.
		trigger := "manual"
		if c.Trigger == "auto" {
			trigger = "automatic"
		}
		r.rawOutput, _ = json.Marshal(struct {
			Trigger    string `json:"trigger"`
			PreTokens  *int   `json:"preTokens,omitempty"`
			PostTokens *int   `json:"postTokens,omitempty"`
			DurationMS *int64 `json:"durationMs,omitempty"`
		}{trigger, c.PreTokens, c.PostTokens, c.DurationMS})
	}
	ts.sendLocked(t, r)
}

// compactEnded is a compaction's outcome when claude says it: a failed
// one fails, saying why; a success ends one its boundary hasn't. Claude
// may say it twice: the second is ignored.
func (ts *tools) compactEnded(result, why string) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	t := ts.compact
	if t == nil || t.ended {
		return
	}
	t.ended = true
	switch {
	case result != "failed" && ts.compactUpdates:
		ts.compactionLocked(t, acp.CompactionStatusCompleted, "")
		return
	case result != "failed":
		ts.sendLocked(t, report{status: acp.ToolCallStatusCompleted})
		return
	}
	ts.failure = why
	if ts.compactUpdates {
		ts.compactionLocked(t, acp.CompactionStatusFailed, why)
		return
	}
	out, _ := json.Marshal(map[string]string{"error": why})
	ts.sendLocked(t, report{status: acp.ToolCallStatusFailed, rawOutput: out, content: texts("Compaction failed: " + why)})
}

// compactFailure says text is claude's second word on why the turn's
// compaction failed, which the compaction's call shows: it is said once.
func (ts *tools) compactFailure(text string) bool {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if ts.failure == "" || strings.TrimSpace(text) != strings.TrimSpace(ts.failure) {
		return false
	}
	ts.failure = ""
	return true
}

// compactionShown says the turn showed a compaction: the result's text
// is claude's word on it, which the call has said.
func (ts *tools) compactionShown() bool {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return ts.compactShown
}

// postCompact is the hook claude runs after a compaction, before it says
// the compaction ended: its summary goes with the end, for a client that
// takes ACP's compaction updates. A subagent's compaction isn't the
// session's.
func (s *session) postCompact(_ context.Context, in claudeagent.HookInput) (claudeagent.HookOutput, error) {
	var c struct {
		Summary string `json:"compact_summary"`
	}
	if in.AgentID == "" && json.Unmarshal(in.Raw, &c) == nil {
		s.tools.summarized(compactionSummary(c.Summary))
	}
	return claudeagent.HookOutput{}, nil
}

// summarized keeps summary for the compaction ending next.
func (ts *tools) summarized(summary string) {
	if summary == "" {
		return
	}
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.summary = summary
}

// compactionSummary is the part of claude's compaction output a user
// reads, as claude-agent-acp takes it: the model's <summary>, without
// the <analysis> before it, or the summary claude framed for the model
// to go on from; "" when neither is found.
func compactionSummary(raw string) string {
	text := analysisLines.ReplaceAllString(raw, "")
	text = analysisLead.ReplaceAllString(text, "")
	switch tagged, ok := taggedSummary(text); {
	case ok:
		text = tagged
	case continuation.MatchString(text):
		body := persistedPreamble.ReplaceAllString(text, "")
		if body == text {
			return "" // claude's framing for the model, not a summary
		}
		text = body
		for _, re := range persistedTrailers {
			text = re.ReplaceAllString(text, "")
		}
	}
	return strings.TrimSpace(text)
}

// taggedSummary is what <summary> tags hold: between the last closing
// tag on a line of its own and the last opening one before it; else the
// widest span of inline tags.
func taggedSummary(raw string) (string, bool) {
	if closes := summaryCloseLine.FindAllStringIndex(raw, -1); closes != nil {
		end := closes[len(closes)-1][0]
		if opens := summaryOpenLine.FindAllStringIndex(raw[:end], -1); opens != nil {
			return raw[opens[len(opens)-1][1]:end], true
		}
	}
	lower := strings.ToLower(raw)
	open, end := strings.Index(lower, "<summary>"), strings.LastIndex(lower, "</summary>")
	if open >= 0 && end > open {
		return raw[open+len("<summary>") : end], true
	}
	return "", false
}

var (
	// analysisLines and analysisLead are the model's reasoning before its
	// summary: tags on lines of their own anywhere, or inline leading it.
	analysisLines = regexp.MustCompile(`(?im)^[ \t]*<analysis>[ \t]*\r?\n[\s\S]*?\n[ \t]*</analysis>[ \t]*$`)
	analysisLead  = regexp.MustCompile(`(?i)^\s*<analysis>[\s\S]*?</analysis>`)
	// summaryOpenLine and summaryCloseLine are <summary> tags on lines of
	// their own, as the model writes them.
	summaryOpenLine  = regexp.MustCompile(`(?im)^[ \t]*<summary>[ \t]*\r?\n`)
	summaryCloseLine = regexp.MustCompile(`(?im)^[ \t]*</summary>[ \t]*$`)
	// continuation, persistedPreamble and persistedTrailers are claude's
	// framing of a summary it keeps for the model to go on from.
	continuation      = regexp.MustCompile(`(?i)^\s*This session is being continued from a previous conversation`)
	persistedPreamble = regexp.MustCompile(`^\s*This session is being continued from a previous conversation[^\n]*\n+Summary:[ \t]*\r?\n`)
	persistedTrailers = []*regexp.Regexp{
		regexp.MustCompile(`\n+If you need specific details from before compaction[\s\S]*$`),
		regexp.MustCompile(`\n+(?:Please )?[Cc]ontinue the conversation from where it left (?:it )?off[\s\S]*$`),
	}
)

// memoryRecall is the memories claude recalled for a turn, as a read
// that has ended.
func (ts *tools) memoryRecall(m *claudeagent.MemoryRecall) {
	var memories []json.RawMessage
	title := "Recalled memory"
	if json.Unmarshal(m.Memories, &memories) == nil {
		title = "Recalled " + strconv.Itoa(len(memories)) + " memories"
		if len(memories) == 1 {
			title = "Recalled 1 memory"
		}
	}
	id := m.UUID
	if id == "" {
		id = "memory-" + ts.newID()
	}
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.sendLocked(&tool{id: id, name: "memory_recall", ended: true},
		report{status: acp.ToolCallStatusCompleted, title: title, kind: acp.ToolKindRead, rawOutput: m.Memories})
}

// localOutput is what a slash command claude ran itself printed (/context
// among them): message text. The turn's result text follows it as a
// second message, as claude-agent-acp sends both; a client that wants
// the command's output reads the last.
func (s *session) localOutput(m *claudeagent.LocalCommandOutput) {
	if m.Content == "" || s.tools.compactFailure(m.Content) {
		return
	}
	s.st.local = true
	s.chunk("agent_message_chunk", "", m.UUID, acp.ContentBlock{Text: &acp.TextContent{Text: m.Content}})
}

// refusal keeps why the model refused a top-level response, from stop
// details ({"explanation": …}) or claude's own words: a turn that ends
// refused says so.
func (s *session) refusal(details json.RawMessage, said string) {
	var d struct {
		Explanation string `json:"explanation"`
	}
	_ = json.Unmarshal(details, &d)
	if text := cmp.Or(d.Explanation, said); text != "" {
		s.st.refused = text
	}
}

// signIn says the message is the one claude writes in place of the
// model's when it isn't signed in.
func signIn(m *claudeagent.Assistant) bool {
	if m.Message.Model != "<synthetic>" {
		return false
	}
	for _, b := range m.Message.Content {
		if strings.Contains(b.Text, "Please run /login") {
			return true
		}
	}
	return false
}
