package claudeacp

import (
	"cmp"
	"context"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	acp "github.com/afrase/acp-go/unstable"

	"github.com/afrase/claude-acp-go/claudeagent"
)

// What claude says of its own, beside the model's words: notices, a
// compaction, recalled memories, a slash command's output, a refusal.

// notice tells the user something of claude's: as ACP's notice to a
// client that shows them, the text's first line its title and the rest
// its description (claude-agent-acp's shape); else as message text with
// no message id, "**Level:** " before it (none for info) and
// _meta.claudeCode saying it is a notice, so a client that knows claude
// can tell it from the model's words.
func (s *session) notice(m *claudeagent.Informational) {
	level, content := m.Level, m.Content
	if content == "" {
		return
	}
	if s.a.clientCaps().notices {
		// A notice client is told what matters only, once per tool use:
		// claude repeats a tool's progress, and a notice can't be updated.
		if level == "info" || m.ToolUseID != "" && s.st.noticed[m.ToolUseID] {
			return
		}
		if m.ToolUseID != "" {
			s.st.noticed[m.ToolUseID] = true
		}
		n := &acp.Notice{Severity: acp.NoticeSeverityInfo}
		fallback := "Claude reported a notice"
		if level == "warning" {
			n.Severity, fallback = acp.NoticeSeverityWarning, "Claude reported a warning"
		}
		n.Title, n.Description = splitNotice(content, fallback)
		s.update(acp.SessionUpdate{SessionUpdate: "notice", Notice: n})
		// A hook-blocked turn's result repeats the notice: the turn
		// keeps what it said, so the result isn't shown again (result).
		s.mu.Lock()
		if t := s.noticedLocked(); t != nil {
			t.notices = append(t.notices, noticeText(content))
		}
		s.mu.Unlock()
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
	// The line is text the turn showed, as in claude-agent-acp: a result
	// that repeats it isn't shown again.
	s.mu.Lock()
	if t := s.noticedLocked(); t != nil {
		t.textSent = true
	}
	s.mu.Unlock()
}

// noticedLocked is the turn a notice is of: the running one, else the
// next.
func (s *session) noticedLocked() *turn {
	if s.active != nil {
		return s.active
	}
	if len(s.turns) > 0 {
		return s.turns[0]
	}
	return nil
}

// noticeText is a notice's text as a result's is compared with it:
// trimmed, each run of white space one space.
func noticeText(text string) string { return strings.Join(strings.Fields(text), " ") }

// modelFallback is claude retrying a refused response on another model,
// said as claude-agent-acp says it: a warning, or text with no message id.
func (s *session) modelFallback(m *claudeagent.ModelRefusalFallback) {
	local := m.Scope == "local"
	outcome := "The session will continue on " + m.FallbackModel + "."
	switch {
	case local:
		outcome = "Only that response came from " + m.FallbackModel + "; the session stays on " + m.OriginalModel + "."
	case m.Direction == "revert":
		outcome = "The session stays on " + m.OriginalModel + "."
	}
	category := ""
	if m.APIRefusalCategory != "" {
		category = " (" + m.APIRefusalCategory + ")"
	}
	summary := m.OriginalModel + " declined this request" + category + "; retried with " + m.FallbackModel + ". " + outcome
	whole := summary
	if m.APIRefusalExplanation != "" {
		whole += "\n\n" + m.APIRefusalExplanation
	}
	if !s.a.clientCaps().notices {
		s.update(acp.SessionUpdate{SessionUpdate: "agent_message_chunk", AgentMessageChunk: &acp.ContentChunk{
			Content: acp.ContentBlock{Text: &acp.TextContent{Text: "**Model fallback:** " + whole}},
		}})
		return
	}
	n := &acp.Notice{Severity: acp.NoticeSeverityWarning, Title: summary, Description: m.APIRefusalExplanation}
	if utf8.RuneCountInString(summary) > maxNoticeTitle {
		n.Title, n.Description = "Model fallback", whole
	}
	s.update(acp.SessionUpdate{SessionUpdate: "notice", Notice: n})
}

// maxNoticeTitle is the longest line that is a notice's title on its
// own, as claude-agent-acp has it.
const maxNoticeTitle = 256

// splitNotice is text as a notice's title and description, as
// claude-agent-acp shapes it: plain text, its first line the title and
// the rest the description; a first line too long for a title makes the
// whole text the description, under fallback.
func splitNotice(text, fallback string) (title, description string) {
	text = strings.TrimSpace(plainText(text))
	if text == "" {
		return fallback, ""
	}
	first, rest, _ := strings.Cut(text, "\n")
	first = strings.TrimRightFunc(first, unicode.IsSpace)
	if utf8.RuneCountInString(first) > maxNoticeTitle {
		return fallback, text
	}
	return first, strings.TrimSpace(rest)
}

// plainText is text without the inline Markdown a notice can't show:
// headings' marks, bold's and code's.
func plainText(text string) string {
	text = headingMark.ReplaceAllString(text, "")
	return unmarked(unmarked(text, unbolded), uncoded)
}

// unmarked is text with each span at matches replaced by what it holds;
// at says the span starting at text[i:], if one does: its end and what
// it holds.
func unmarked(text string, at func(text string, i int) (end int, inner string, ok bool)) string {
	var b strings.Builder
	for i := 0; i < len(text); {
		if end, inner, ok := at(text, i); ok {
			b.WriteString(inner)
			i = end
			continue
		}
		b.WriteByte(text[i])
		i++
	}
	return b.String()
}

// unbolded finds bold at text[i:]: ** or __, text that starts and ends
// with no space, and the same mark.
func unbolded(text string, i int) (int, string, bool) {
	rest := text[i:]
	if !strings.HasPrefix(rest, "**") && !strings.HasPrefix(rest, "__") {
		return 0, "", false
	}
	mark := rest[:2]
	if r, _ := utf8.DecodeRuneInString(rest[2:]); rest[2:] == "" || unicode.IsSpace(r) {
		return 0, "", false
	}
	for j := 3; j+2 <= len(rest); j++ {
		if r, _ := utf8.DecodeLastRuneInString(rest[:j]); rest[j:j+2] == mark && !unicode.IsSpace(r) {
			return i + j + 2, rest[2:j], true
		}
	}
	return 0, "", false
}

// uncoded finds inline code at text[i:]: a backtick, text of one line,
// and a backtick, with no word or backtick against either.
func uncoded(text string, i int) (int, string, bool) {
	if text[i] != '`' || i > 0 && wordOrTick(text[i-1]) {
		return 0, "", false
	}
	n := strings.IndexAny(text[i+1:], "`\n")
	if n <= 0 || text[i+1+n] != '`' {
		return 0, "", false
	}
	end := i + 1 + n + 1
	if end < len(text) && wordOrTick(text[end]) {
		return 0, "", false
	}
	return end, text[i+1 : end-1], true
}

// wordOrTick says c is a word's (ASCII letter, digit or _) or a backtick.
func wordOrTick(c byte) bool {
	return c == '_' || c == '`' || '0' <= c && c <= '9' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z'
}

// headingMark is a Markdown heading's #s, at a line's start.
var headingMark = regexp.MustCompile(`(?m)^[ \t]*#{1,6}[ \t]+`)

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
// that has ended, as claude-agent-acp shows it: the memory files it
// read, or what claude made of them (mode "synthesize"), which no file
// holds.
func (ts *tools) memoryRecall(m *claudeagent.MemoryRecall) {
	var memories []struct {
		Path    string          `json:"path"`
		Content json.RawMessage `json:"content"`
	}
	r := report{status: acp.ToolCallStatusCompleted, title: "Recalled memory", kind: acp.ToolKindRead, rawOutput: m.Memories}
	r.response, _ = json.Marshal(struct {
		Mode string `json:"mode,omitempty"`
	}{m.Mode})
	if json.Unmarshal(m.Memories, &memories) == nil {
		r.title = "Recalled " + strconv.Itoa(len(memories)) + " memories"
		if len(memories) == 1 {
			r.title = "Recalled 1 memory"
		}
	}
	if m.Mode == "synthesize" {
		r.title = "Recalled synthesized memory"
	}
	for _, mem := range memories {
		var text string
		switch {
		case m.Mode != "synthesize":
			r.locations = append(r.locations, acp.ToolCallLocation{Path: mem.Path})
		case json.Unmarshal(mem.Content, &text) == nil:
			r.content = append(r.content, textContent(text))
		}
	}
	id := m.UUID
	if id == "" {
		id = "memory-" + ts.newID()
	}
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.sendLocked(&tool{id: id, name: "memory_recall", ended: true}, r)
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
// details ({"explanation": …}), or claude's own words when nothing has
// said: a turn that ends refused says so. The response's own explanation
// wins whichever comes first, as in claude-agent-acp.
func (s *session) refusal(details json.RawMessage, said string) {
	var d struct {
		Explanation string `json:"explanation"`
	}
	_ = json.Unmarshal(details, &d)
	switch {
	case d.Explanation != "":
		s.st.refused = d.Explanation
	case s.st.refused == "":
		s.st.refused = said
	}
}

// refusedNoFallback is claude's word that a refused response has no
// model to retry on. It speaks for the user's turn unless claude says
// that no user message was refused (null: a turn claude started itself,
// a background task's follow-up), whose explanation isn't the user's.
func (s *session) refusedNoFallback(m *claudeagent.ModelRefusalNoFallback) {
	var whose struct {
		UUID json.RawMessage `json:"refused_user_message_uuid"`
	}
	if json.Unmarshal(m.Raw, &whose) == nil && string(whose.UUID) == "null" {
		return
	}
	s.refusal(nil, cmp.Or(m.APIRefusalExplanation, m.Content))
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
