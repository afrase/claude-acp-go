// Derived from claude-agent-acp (https://github.com/agentclientprotocol/claude-agent-acp),
// v0.94.0, its list of the markers a replayed user message is cleaned of,
// how it removes them, and the commands it hides (dist/acp-agent.js,
// stripLocalCommandMetadata).
// Copyright 2025 Zed Industries, Inc. and contributors. Licensed under the
// Apache License, Version 2.0 (see NOTICE). Modified: rewritten in Go for
// claudeacp.

package claudeacp

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// What claude writes into a user message besides what the user typed: a
// slash command it ran as tags naming it, the command's output, and
// context it adds (reminders, tasks' ends). A replay shows the user's
// words, and a command as the user typed it.

// markers are the tags whose text the user didn't write, each removed
// whole: a command's tags and output, and context claude adds.
var markers = []string{"command-name", "command-message", "command-args", "local-command-stdout", "local-command-stderr",
	"local-command-caveat", "system-reminder", "task-notification"}

// hiddenReplay are the commands a replay leaves out: what they printed
// isn't replayed, so the command alone would say nothing.
var hiddenReplay = []string{"/context", "/heapdump", "/extra-usage", "/compact", "/model", "/mcp", "/status", "/usage"}

// userText is a user message's text as the user wrote it, without the
// markers; a message of markers alone is the command it ran, as typed.
// false when nothing of it is the user's, or it is a command a replay
// hides.
func userText(text string) (string, bool) {
	if s := strings.TrimSpace(stripMarkers(text)); s != "" {
		return s, true
	}
	return commandLine(text)
}

// stripMarkers removes each marker from text, an opening tag with the
// nearest closing tag of its own name; an opening tag with none after it
// is the user's text, as claude-agent-acp's stripMarkerTags has it.
func stripMarkers(text string) string {
	var b strings.Builder
	unclosed := map[string]bool{}
	copied := 0
	for i := 0; i < len(text); i++ {
		if text[i] != '<' {
			continue
		}
		for _, tag := range markers {
			open, end := "<"+tag+">", "</"+tag+">"
			if unclosed[tag] || !strings.HasPrefix(text[i:], open) {
				continue
			}
			n := strings.Index(text[i+len(open):], end)
			if n < 0 {
				unclosed[tag] = true
				break
			}
			b.WriteString(text[copied:i])
			copied = i + len(open) + n + len(end)
			i = copied - 1
			break
		}
	}
	b.WriteString(text[copied:])
	return b.String()
}

// commandLine is the command a message of markers ran, as the user typed
// it: its name and arguments. false when it printed what isn't replayed,
// names no command, or names one a replay hides.
func commandLine(text string) (string, bool) {
	if _, ok := markerText(text, "local-command-stdout"); ok {
		return "", false
	}
	if _, ok := markerText(text, "local-command-stderr"); ok {
		return "", false
	}
	name, _ := markerText(text, "command-name")
	name = strings.TrimSpace(name)
	first, _, _ := strings.Cut(name, " ")
	if !strings.HasPrefix(name, "/") || slices.Contains(hiddenReplay, first) {
		return "", false
	}
	if args, _ := markerText(text, "command-args"); strings.TrimSpace(args) != "" {
		name += " " + strings.TrimSpace(args)
	}
	return name, true
}

// markerText is the text of the first marker tag in text.
func markerText(text, tag string) (string, bool) {
	_, after, ok := strings.Cut(text, "<"+tag+">")
	if !ok {
		return "", false
	}
	if inner, _, ok := strings.Cut(after, "</"+tag+">"); ok {
		return inner, true
	}
	return "", false
}

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
