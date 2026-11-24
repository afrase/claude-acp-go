package claudeacp

import (
	"regexp"
	"strconv"
	"strings"
)

// What a task notification in a user message says of a task's end. The
// user's own text in a message is sessions.UserText's.

var (
	notification = regexp.MustCompile(`(?s)<task-notification>(.*?)</task-notification>`)
	taskID       = regexp.MustCompile(`(?s)<task-id>(.*?)</task-id>`)
	taskStatus   = regexp.MustCompile(`(?s)<status>(.*?)</status>`)
	taskSummary  = regexp.MustCompile(`(?s)<summary>(.*?)</summary>`)
	taskOutput   = regexp.MustCompile(`(?s)<output-file>(.*?)</output-file>`)
	taskCall     = regexp.MustCompile(`(?s)<tool-use-id>(.*?)</tool-use-id>`)
	taskUsage    = regexp.MustCompile(`(?s)<usage>(.*?)</usage>`)
	usageTokens  = regexp.MustCompile(`<subagent_tokens>\s*(\d+)\s*</subagent_tokens>`)
	usageTools   = regexp.MustCompile(`<tool_uses>\s*(\d+)\s*</tool_uses>`)
	usageTime    = regexp.MustCompile(`<duration_ms>\s*(\d+)\s*</duration_ms>`)
)

// taskNote is what a task notification says of a task's end: call is
// the call that started the task, and usage what a subagent used.
type taskNote struct {
	id, call, status, summary, outputFile string
	usage                                 *totals
}

// taskEnds are the task notifications in a user message's text, in order.
func taskEnds(text string) []taskNote {
	var out []taskNote
	for _, n := range notification.FindAllStringSubmatch(text, -1) {
		field := func(re *regexp.Regexp) string { return submatch(re, n[1]) }
		if id := field(taskID); id != "" {
			out = append(out, taskNote{id: id, call: field(taskCall), status: field(taskStatus), summary: field(taskSummary),
				outputFile: field(taskOutput), usage: usageOf(field(taskUsage))})
		}
	}
	return out
}

// submatch is re's first group in text, trimmed; "" when re isn't there.
func submatch(re *regexp.Regexp, text string) string {
	if m := re.FindStringSubmatch(text); m != nil {
		return strings.TrimSpace(m[1])
	}
	return ""
}

// usageOf is what a subagent used, as a task notification's <usage>
// says it: <subagent_tokens>, <tool_uses> and <duration_ms>; nil without
// the tokens.
func usageOf(usage string) *totals {
	tokens, err := strconv.Atoi(submatch(usageTokens, usage))
	if err != nil {
		return nil
	}
	uses, _ := strconv.Atoi(submatch(usageTools, usage))
	ms, _ := strconv.ParseInt(submatch(usageTime, usage), 10, 64)
	return &totals{TotalTokens: tokens, TotalToolUseCount: uses, TotalDurationMs: ms}
}
