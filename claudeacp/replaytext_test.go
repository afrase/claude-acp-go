package claudeacp

import "testing"

// A replayed user message loses the markers claude wrote into it and
// keeps what the user typed, as claude-agent-acp's replay keeps it: a
// marker closes only with its own closing tag, an opening tag with none
// after it is the user's, and a message is a command only when nothing
// but markers is in it.
func TestUserText(t *testing.T) {
	for _, tc := range []struct {
		in, want string
		ok       bool
	}{
		{"Fix the bug", "Fix the bug", true},
		{"hi<system-reminder>r</system-reminder>", "hi", true},
		{"why does <task-notification> show?<system-reminder>r</system-reminder>", "why does <task-notification> show?", true},
		{"what does <command-name>/foo</command-name> mean in my transcript?", "what does  mean in my transcript?", true},
		{"<command-name>/foo</command-name>hi", "hi", true},
		{"<command-message>review</command-message>\n<command-name>/review</command-name>\n<command-args>main</command-args>", "/review main", true},
		{"<command-name>/context</command-name><command-message>context</command-message><command-args></command-args>", "", false},
		{"<command-name>review</command-name>", "", false},
		{"<command-name>/doctor</command-name><local-command-stdout>ok</local-command-stdout>", "", false},
		{"<local-command-stdout>## Context Usage</local-command-stdout>", "", false},
		{"<system-reminder>a</task-notification>b</system-reminder>c", "c", true},
	} {
		if got, ok := userText(tc.in); got != tc.want || ok != tc.ok {
			t.Errorf("userText(%q) = %q, %v; want %q, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}
