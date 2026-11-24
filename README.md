# claude-acp-go

[![Go Reference](https://pkg.go.dev/badge/github.com/afrase/claude-acp-go.svg)](https://pkg.go.dev/github.com/afrase/claude-acp-go)
[![CI](https://github.com/afrase/claude-acp-go/actions/workflows/ci.yml/badge.svg)](https://github.com/afrase/claude-acp-go/actions/workflows/ci.yml)

Run Claude Code in Zed and other [Agent Client Protocol](https://agentclientprotocol.com)
(ACP) clients, without Node.

`claude-acp` is a single binary. It speaks ACP to the editor and runs
your own `claude` CLI for each session. It behaves like
[claude-agent-acp](https://github.com/agentclientprotocol/claude-agent-acp),
the Node agent that ACP clients use today, so a client that works with one
works with the other.

This module also holds the Go libraries that `claude-acp` is built on (see
[Use it as a library](#use-it-as-a-library)).

> [!NOTE]
> This project is unofficial. It is not affiliated with or endorsed by
> Anthropic. It has no authentication code and never touches your
> credentials. It runs the unmodified `claude` that you installed, which
> signs in as usual.

## Why

- **No Node.** claude-agent-acp needs Node, an npm install, and a Node
  process next to each `claude`. `claude-acp` is one Go binary.
- **Your claude.** It runs the `claude` on your `PATH` (or
  `$CLAUDE_CODE_EXECUTABLE`). Your login, settings, `CLAUDE.md`, hooks,
  skills, plugins and MCP servers all apply, as in the terminal.
- **Shared sessions.** Sessions are claude's own. Start one here and resume
  it in the terminal with `claude --resume`, or the other way round.
- **Readable.** The protocol code is plain Go with no bundled SDK. You can
  read it and step through it.

## Install

You need:

- [Claude Code](https://code.claude.com/docs/en/setup), installed and signed
  in (run `claude` once in a terminal).
- Go 1.27.4 or later, to build.

```sh
go install github.com/afrase/claude-acp-go/cmd/claude-acp@latest
```

The binary goes to `$(go env GOPATH)/bin`. Run `claude-acp -version` to
check it.

On Linux, `go install` links the binary to the C library when a C
compiler is present. To copy it to another machine, such as an Alpine
container or an older distribution, build a static one instead:

```sh
CGO_ENABLED=0 go install github.com/afrase/claude-acp-go/cmd/claude-acp@latest
```

## Use it from Zed

1. Add the agent to Zed's `settings.json`. Use the binary's full path,
   because Zed does not start agents through your shell.

   ```json
   {
     "agent_servers": {
       "Claude (claude-acp-go)": {
         "type": "custom",
         "command": "/Users/you/go/bin/claude-acp",
         "args": [],
         "env": {}
       }
     }
   }
   ```

2. In the Agent Panel, open the new-thread menu and pick the agent.

If `claude` is not on the `PATH` that Zed gives agents, set it in `env`:
`"env": {"CLAUDE_CODE_EXECUTABLE": "/opt/homebrew/bin/claude"}`.

**Other ACP clients** start it the same way: run `claude-acp` and speak ACP
on its stdin and stdout. It logs to stderr. Its own log lines never hold a
prompt or a token, but it passes on what `claude` prints on its stderr,
which may quote one (an error, or debug output you turned on).

## Use it as a library

The module has four packages:

| Package | What it is |
|---|---|
| `claudeagent` | A client for the `claude` CLI's stream-JSON control protocol, the one Anthropic's Agent SDKs use. Sessions, one-shot queries, typed messages, permission callbacks, hooks, MCP elicitations and control requests. Standard library only. |
| `claudeagent/sessions` | claude's transcripts on disk: list, read, fork and delete sessions. |
| `claudeagent/claudetest` | A scripted fake `claude` and recorded cassettes. Test code built on `claudeagent` with no process and no login. |
| `claudeacp` | The ACP agent, built on `claudeagent` and [acp-go](https://github.com/afrase/acp-go). `Serve` it on any streams, or call `Main` for stdin and stdout. |

One prompt, its answer and its cost:

```go
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/afrase/claude-acp-go/claudeagent"
)

func main() {
	ctx := context.Background()
	for m, err := range claudeagent.Query(ctx, "What does this repository do?", claudeagent.Options{}) {
		if err != nil {
			log.Fatal(err)
		}
		switch m := m.(type) {
		case *claudeagent.Assistant:
			for _, b := range m.Message.Content {
				if b.Type == "text" {
					fmt.Print(b.Text)
				}
			}
		case *claudeagent.Result:
			fmt.Printf("\n(%d turns, $%.4f)\n", m.NumTurns, m.TotalCostUSD)
		}
	}
}
```

- `claudeagent.Start` keeps a session open for more prompts, steering,
  interrupts, and model or mode changes.
- `Options.CanUseTool` answers claude's permission requests.
- Each package's documentation has runnable examples:
  `go doc github.com/afrase/claude-acp-go/claudeagent`.

## What's supported

| Area | Support |
|---|---|
| Sessions | New, load (replays the history), resume, fork (whole, or up to a message), list, delete, close. Additional directories. A system prompt to append or to replace. |
| Prompts | Text, images and embedded resources. A prompt sent during a turn is queued as `claude` queues it, or steers the running turn (`_session/steering`). Cancel. |
| Permissions | Every permission request from `claude` goes to the client, with "always allow" options worded for each tool. The agent never allows anything itself. |
| Modes | Default, accept edits, plan, auto, and bypass permissions (when allowed). |
| Config options | Model, effort, fast mode, agent. |
| Turn output | Plans from claude's to-do tools. AskUserQuestion as a form (if the client takes form elicitation). Slash commands. Usage, cost and the context window. Session titles. claude's own notices. |
| MCP | MCP servers from the client (stdio, HTTP, SSE). An MCP server's form or sign-in page goes to the client, if the client takes them. |
| Subagents | Each subagent is a session of its own (the ACP subagents RFD), and each can be stopped, if the client takes them. |
| Background commands | When a command claude left running starts and ends, on its Bash call (see [Background commands](#background-commands)). |
| Background subagents | What one used (tokens, tool calls, time), on its Agent call as it ends (see [Background subagents](#background-subagents)). |

Not supported:

- **ACP's `authenticate`.** There is nothing to authenticate: sign in with
  `claude` itself. If `claude` is not signed in, prompts fail with its own
  message.
- **MCP servers over ACP's own transport.**
- **The client's file-system and terminal capabilities.** claude's tools run
  on the machine, as in the terminal.
- **Windows** is not tested. The module builds there (CI checks that), but
  the tests run on macOS and Linux only.

### Extension methods

Besides ACP's own methods (and `_session/steering`, under Prompts), the
agent answers these extension methods. Each takes an open session's id
(`sessionId`); one that isn't open is an invalid-params error. Each can
be called while a turn runs, and none starts one.

| Method | Params | Answer |
|---|---|---|
| `_session/generateTitle` | `sessionId`, `text` | `{"title": …}`: a short title for any text, made by claude's fast model. The session's own title is left as it was. |
| `_session/contextUsage` | `sessionId`, `detail` | How the context window is spent: claude's `get_context_usage` answer, whole. |

`_session/contextUsage`'s `detail` is claude's own: `"summary"`,
`"full"`, or absent for claude's default. The answer is passed through as
claude sent it. Its typed part, as `claudeagent.ContextUsage` decodes it,
is `categories` (each a `name`, `tokens` and `kind`: `used`, `free`,
`buffer` or `deferred`), `totalTokens`, `maxTokens`, `rawMaxTokens`,
`percentage` and `model`. Anything else, such as the lists a `"full"`
answer carries, is claude's undocumented own: claude may add, rename or
reword it. Classify a category on `kind`, never on its English `name`.
Nothing is forked and nothing is written to the transcript.

### Background commands

A Bash call with `run_in_background` completes at once, but its command
runs on as a claude task. Each change of that task goes to the client as
a `tool_call_update` of the Bash call, with only `_meta`:

```json
{"sessionUpdate": "tool_call_update", "toolCallId": "toolu_…",
 "_meta": {"claudeCode": {"toolName": "Bash", "task": {
   "id": "bqphuo3vw", "type": "local_bash", "status": "completed",
   "outputFile": "/tmp/claude-<uid>/…/tasks/bqphuo3vw.output",
   "summary": "Background command \"Finish soon\" completed (exit code 0)",
   "endTime": 1793314896880}}}}
```

- `id` is the task's id, the same as the call's
  `toolResponse.backgroundTaskId`.
- `status` is `running` when claude starts the task, then claude's word
  for its end: `completed`; `failed` (a non-zero exit, or killed from
  outside); `killed` then `stopped` for claude's `TaskStop`.
- `endTime` (milliseconds since the epoch) comes with the end. `outputFile`
  and `summary` come with claude's notification, a moment later. claude
  gives the exit code only in `summary`'s text.
- An update goes for each change, in order with the turn's other
  updates. A subagent's command's updates name its Agent call in
  `parentToolUseId`, as its other updates do.
- The call's own `status` doesn't change: it stays `completed`.
- Every client gets them; no capability is needed. claude-agent-acp
  tells only JetBrains AIR clients of these tasks (`async_task_*`).
- `session/load` replays a command's end where the history has it
  (claude's notification of it, or its `TaskStop`). The history doesn't
  say that a command still runs, so its start is replayed only for a
  session that is open, whose claude still runs it.

### Background subagents

A foreground subagent's Agent call ends with what the subagent used in
its `toolResponse` (`totalTokens`, `totalToolUseCount`,
`totalDurationMs`). A background one's call ends at once, its
`toolResponse` saying only `"status": "async_launched"`; claude tells what
it used only in its task notification, as it ends. That goes to the
client as a `tool_call_update` of the Agent call, with only `_meta`, under
the same names:

```json
{"sessionUpdate": "tool_call_update", "toolCallId": "toolu_…",
 "_meta": {"claudeCode": {"toolName": "Agent", "toolResponse": {
   "totalTokens": 8152, "totalToolUseCount": 2, "totalDurationMs": 3175}}}}
```

- It comes once, with claude's notification of the subagent's end, and
  only when that has the usage. Every client gets it; no capability is
  needed.
- A subagent's subagent's goes where its Agent call's updates go, naming
  its stream in `parentToolUseId`.
- `session/load` replays it from the `<usage>` of claude's
  `<task-notification>` in the history.

## How it relates to the others

- **claude-agent-acp** (Zed Industries, Apache-2.0) is the Node ACP agent
  for Claude Code. `claudeacp` follows its behaviour and wire shape, so
  clients need no changes: session and message ids, `_meta.claudeCode`,
  `_claude/origin`, modes, config options, and the JetBrains fork-at
  extension. The code is written new in Go. A file translated from
  claude-agent-acp says so, and `claudeacp/NOTICE` carries the licence.
- **Anthropic's Agent SDKs** (Python, MIT; TypeScript) drive `claude` over
  the same stream-JSON protocol. `claudeagent` is a Go client for that
  protocol. `claudeagent/sessions` is ported from the Python SDK's session
  helpers (`claudeagent/NOTICE`). Nothing comes from the TypeScript SDK or
  from the `claude` binary.

The stream-JSON protocol is undocumented. To limit breakage, `claudeagent`:

- parses leniently: an unknown message arrives as `Unknown`, and the raw
  JSON is kept;
- detects features from what `claude` says it supports;
- pins no `claude` version.

A change in `claude` can still break it, as it can break the official SDKs.

## Development

```sh
make check   # build, vet, lint, govulncheck, the tests with -race
```

golangci-lint and govulncheck are pinned in `tools/lint` and run through
`go tool`, so you install nothing.

The tests run fake `claude`s and need no login. To record new cassettes,
`claudeagent/claudetest/cmd` has tools that run the real `claude` against a
fake Messages API (`fakeapi`).

## Licence and credits

MIT: see [LICENSE](LICENSE). Parts are derived from:

- the [Claude Agent SDK for Python](https://github.com/anthropics/claude-agent-sdk-python),
  MIT, Copyright (c) 2025 Anthropic, PBC (`claudeagent/NOTICE`);
- [claude-agent-acp](https://github.com/agentclientprotocol/claude-agent-acp),
  Apache-2.0, Copyright 2025 Zed Industries, Inc. and contributors
  (`claudeacp/NOTICE`).

Claude and Claude Code are trademarks of Anthropic, PBC. When you use this
software, you use Claude Code, under the terms of your Anthropic account.
