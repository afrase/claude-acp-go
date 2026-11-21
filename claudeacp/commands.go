package claudeacp

import (
	"slices"
	"strings"
	"sync"

	acp "github.com/afrase/acp-go/unstable"

	"github.com/afrase/claude-acp-go/claudeagent"
)

// A session's slash commands are claude's, from its answer to initialize
// and each commands_changed after: the client offers them, and a prompt
// that starts with one runs it.

// hiddenCommands are claude's commands a client has no use for: signing
// in and out, its terminal's screens and keys, clearing (a client starts
// a new session instead), and /mcp, whose changes need claude's terminal.
var hiddenCommands = []string{"clear", "cost", "keybindings-help", "login", "logout", "mcp", "output-style:new", "release-notes", "todos"}

// commands are the slash commands the client was told of.
type commands struct {
	send func(acp.SessionUpdate) // to the client, on the session

	mu       sync.Mutex
	list     []claudeagent.Command
	terminal []string // the ones claude runs only in its terminal, from system/init
}

// sendAll tells the client of the session's commands, all of them. It
// holds the lock while it sends, so the client's last list is the last
// one claude gave.
func (c *commands) sendAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := []acp.AvailableCommand{}
	for _, cmd := range c.list {
		if slices.Contains(hiddenCommands, cmd.Name) || slices.Contains(c.terminal, cmd.Name) {
			continue
		}
		ac := acp.AvailableCommand{Name: clientName(cmd.Name), Description: cmd.Description}
		if cmd.ArgumentHint != "" {
			ac.Input = &acp.AvailableCommandInput{Unstructured: &acp.UnstructuredCommandInput{Hint: cmd.ArgumentHint}}
		}
		out = append(out, ac)
	}
	c.send(acp.SessionUpdate{SessionUpdate: "available_commands_update",
		AvailableCommandsUpdate: &acp.AvailableCommandsUpdate{AvailableCommands: out}})
}

// clientName is a command's name as claude-agent-acp gives it: an MCP
// server's prompt, which claude names "server:prompt (MCP)", is
// "mcp:server:prompt", a name with no space; promptToClaude turns it
// back.
func clientName(name string) string {
	if strings.HasSuffix(name, mcpSuffix) {
		return "mcp:" + strings.Replace(name, mcpSuffix, "", 1)
	}
	return name
}

// mcpSuffix ends the name claude gives an MCP server's prompt.
const mcpSuffix = " (MCP)"

// changed is claude's new list of commands.
func (c *commands) changed(list []claudeagent.Command) {
	c.mu.Lock()
	c.list = list
	c.mu.Unlock()
	c.sendAll()
}

// terminalOnly follows system/init's list of the commands only claude's
// terminal runs: when it changes, so does the client's.
func (c *commands) terminalOnly(names []string) {
	names = slices.Clone(names)
	for i, n := range names {
		names[i] = strings.TrimPrefix(n, "/")
	}
	c.mu.Lock()
	news := names != nil && !slices.Equal(names, c.terminal)
	if news {
		c.terminal = names
	}
	c.mu.Unlock()
	if news {
		c.sendAll()
	}
}
