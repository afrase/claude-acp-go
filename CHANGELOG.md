# Changelog

All notable changes to this module. It follows [Semantic Versioning](https://semver.org); until 1.0, a minor version may change the API.

## Unreleased

- `claudeacp`: a Write whose content claude took as `file_text` or `file_content`, and a Bash call whose description came as `command_description`, show that text, as claude-agent-acp shows it.
- `claudeacp`: a Read of an image shows the image and a Read of a PDF a line naming the document, where an empty code block showed; a command's image output shows too. An API server tool's result (web search, web fetch, tool search, code execution) says what it found, where the call had no content.
- `claudeacp`: a tool's result shows the tool's own output where claude gives it (`tool_use_result`), as claude-agent-acp does, live and in a loaded session: a command's output without claude's wrapper for the model (output too long ends with where it was saved), a file read without claude's notes, a web search's hits as `Title (url)` lines, and a subagent's report.
- `claudeacp`: a subagent's report loses claude's frame also when claude's notes come above it (a turn limit, output saved), and its `agentId: … (…)` line also when unframed; a turn limit's note shows as `[Agent stopped at its turn limit — the output below is partial]`. Only a trailing `<usage>` block is cut.
- `claudeacp`: an approved ExitPlanMode call keeps showing the plan, retitled `Exited Plan Mode`, and a Skill call keeps its content, where claude's result text for the model replaced them; a refused plan's reason is unfenced in `rawOutput`.
- `claudeacp`: an Edit's or Write's diffs from claude's response say the line each hunk starts at; a Write over a file whose response has no hunks shows the old text against the new, or says the old was too large to diff, where it showed a new file being created.
- `claudeacp`: each of claude's progress beats for a running tool goes to the client with `_meta.claudeCode.toolResponse` `{elapsedTimeSeconds, subagentType?, subagentRetry?}`, where only the first went, bare; a beat under an id of its own (a heartbeat, a subagent's retries) goes to its parent call. `claudeagent.ToolProgress` has `SubagentType` and `SubagentRetry`.

## v0.3.1 (2026-11-14)

- Needs Go 1.27.4, which fixes GO-2026-6859 in `net/http`. `go install` then builds with the fixed standard library.
- The README is in shorter sentences, with what's supported in a table.

## v0.3.0 (2026-11-10)

- `claudeagent/sessions`: `ReadInfo(path)` tells a session's details from the transcript file it is given, as `Get` tells them. `Get` with no folder takes the first project folder that has the session, where `List` keeps the newest; a caller that walks the folders itself reads the one it means.
- `claudeagent/sessions`: `List` no longer waits forever on a named pipe called `<id>.jsonl` in a project folder: only a regular file is opened.

## v0.2.1 (2026-11-05)

- `claudeagent.Version` (and `claudeacp.Version`) is the version the Go build recorded for this module, not a constant: v0.2.0 still said 0.1.0. A build with no record (a checkout's test, `go run`) says `devel`. They are variables now.

## v0.2.0 (2026-11-05)

- `claudeacp`: a `_session/generateTitle` extension method (`GenerateTitleMethod`) that asks claude for a short title for any text, with its fast model and `persist: false`, so the session keeps its own title. A client titles a message with it, a pinned one say.
- `claudeacp`: replayed tool calls and message chunks are built the way the live stream builds them, so a loaded session shows what the live one did.
- `claudeagent/sessions`: a queued prompt's text and a fork's title are read the way `List` reads them; the chain and a subagent's transcript are walked back the same way; the standard library's `uuid` and `cmp.Or` replace local helpers.
- `claudeagent/claudetest`: the `Step` constructors are in `steps.go`; an expected stdin line is read once, for `Expect` and a cassette's steps alike.
- Internal: a session's open requests are counted by its open set; claude's session is kept on the config rather than behind a one-method interface.

## v0.1.0 (2026-11-05)

The first release.

- `claudeagent`: a client for the claude CLI's stream-JSON control protocol: sessions (`Start`) and one-shot queries (`Query`), typed messages with their raw JSON kept, permissions (`CanUseTool`), hooks, MCP elicitations, and claude's control requests (model, permission mode, interrupt, MCP servers, context usage, file rewind, flag settings).
- `claudeagent/sessions`: claude's transcripts on disk: list, details, messages, subagents, queued prompts, fork (whole or up to a message) and delete. Ported from the Claude Agent SDK for Python.
- `claudeagent/claudetest`: a scripted fake claude and cassettes of recorded runs, for tests with no process or login; `fakeapi`, a fake Messages API, and the recording tools under `cmd`.
- `claudeacp`: an ACP agent on claudeagent with claude-agent-acp's wire shape: sessions (new, load, resume, fork, list, delete, close), prompt queueing and steering, cancel, permissions with "always allow", modes and config options (model, effort, fast mode, agent), plans, AskUserQuestion as a form, MCP servers from the client, subagents as sessions of their own, notices, slash commands, usage and context window, session titles.
- `cmd/claude-acp`: the agent as a program, for Zed and other ACP clients.
