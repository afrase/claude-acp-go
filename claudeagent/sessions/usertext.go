// Derived from claude-agent-acp (https://github.com/agentclientprotocol/claude-agent-acp),
// v0.94.0, its list of the markers a replayed user message is cleaned of,
// how it removes them, and the commands it hides (dist/acp-agent.js,
// stripLocalCommandMetadata).
// Copyright 2025 Zed Industries, Inc. and contributors. Licensed under the
// Apache License, Version 2.0 (see ../NOTICE). Modified: rewritten in Go.

package sessions

import (
	"slices"
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

// hiddenReplay are the commands UserText leaves out: what they printed
// isn't replayed, so the command alone would say nothing.
var hiddenReplay = []string{"/context", "/heapdump", "/extra-usage", "/compact", "/model", "/mcp", "/status", "/usage"}

// UserText is a user message's text as the user wrote it, as a replay of
// the session shows it (claudeacp's does). It is for a client that reads
// claude's transcripts itself (a search, say) and must see what a replay
// shows.
//
// claude's markers are removed, each whole: command-name, command-message,
// command-args, local-command-stdout, local-command-stderr,
// local-command-caveat, system-reminder and task-notification. A marker
// runs from its opening tag to the nearest closing tag of its own name; an
// opening tag with no closing tag after it is the user's text, and stays.
// What is left is trimmed. A message of markers alone is the slash command
// it ran, as the user typed it: its name and its arguments.
//
// false when nothing of it is the user's: only markers, and no command,
// or a command that printed output (local-command-stdout or -stderr),
// whose output isn't the user's either, or one of the commands a replay
// leaves out (/context, /heapdump, /extra-usage, /compact, /model, /mcp,
// /status, /usage).
//
// A shell command the user ran with "!" is left in on purpose, tags and
// all (<bash-input>, <bash-stdout>, <bash-stderr>): a client renders those
// as a run.
func UserText(text string) (string, bool) {
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
