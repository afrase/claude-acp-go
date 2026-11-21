package claudeacp

import (
	"strings"
	"testing"

	"github.com/afrase/claude-acp-go/claudeagent/claudetest"
)

// commandNames are the commands of each available_commands_update so
// far, as name(hint) lists.
func (c *client) commandNames() []string {
	var out []string
	for _, u := range c.of("available_commands_update") {
		var names []string
		for _, cmd := range u.AvailableCommandsUpdate.AvailableCommands {
			n := cmd.Name
			if cmd.Input != nil && cmd.Input.Unstructured != nil {
				n += "(" + cmd.Input.Unstructured.Hint + ")"
			}
			names = append(names, n)
		}
		out = append(out, strings.Join(names, " "))
	}
	return out
}

// A new session's commands are claude's but for the ones a client has no
// use for, an MCP server's prompt named as claude-agent-acp names it; they go again when claude's change, and when a turn says which
// only claude's terminal runs.
func TestCommands(t *testing.T) {
	read := make(chan struct{})
	f := started(`{"commands":[{"name":"compact","description":"Compact the conversation","argumentHint":"<instructions>"},`+
		`{"name":"login","description":"Sign in"},{"name":"review","description":"Review a change"},{"name":"mcp","description":"MCP"},`+
		`{"name":"github:create_issue (MCP)","description":"Open an issue"}]}`, `null`,
		user("id2"), echo("id2"),
		claudetest.Send(`{"type":"system","subtype":"commands_changed","commands":[{"name":"review","description":"Review"},{"name":"init","description":"Set up"}]}`),
		claudetest.Send(`{"type":"system","subtype":"init","model":"m","terminal_slash_commands":["/init"]}`),
		claudetest.Send(`{"type":"system","subtype":"init","model":"m","terminal_slash_commands":["/init"]}`),
		result(""), claudetest.Send(idle), signal(read))
	p := start(t, nil, f)
	sid := p.open(t)
	eventually(t, "the commands", func() bool { return len(p.commandNames()) == 1 })
	if got := p.commandNames()[0]; got != "compact(<instructions>) review mcp:github:create_issue" {
		t.Errorf("%s", got)
	}
	stopReason(p.prompt(t, sid, "go"))
	wait(t, read, "the script")
	if got := p.commandNames(); strings.Join(got, "|") != "compact(<instructions>) review mcp:github:create_issue|review init|review" {
		t.Errorf("%q", got)
	}
}
