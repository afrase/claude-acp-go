# Changelog

All notable changes to this module. It follows [Semantic Versioning](https://semver.org); until 1.0, a minor version may change the API.

## Unreleased

- `claudeacp`: without subagent sessions, a subagent that ended and runs again (a SendMessage, a new start, a running update) counts as running: its next end ends the calls that run left open, and a cancel of the turn spares it while it is in the background.
- `claudeacp`: a subagent's task frame is late only when its run id sorts before the run known, as the SDK has run ids; a newer run is adopted. A subagent claude resumes with no new `task_started` (a running `task_updated`, or a SendMessage's result) now ends at that run's end, and its open calls with it; a late `task_started` of an earlier run is ignored.
- `claudeacp`: cancelling a subagent's session also withdraws what the foreground subagents under it ask, as claude stops them with it, so they no longer show running again before they end.
- `claudeacp`: a subagent's tool call whose `tool_call` went on the session (held past the bounds before claude named the subagent) has its later updates go there too, not to the child, so it is no longer left pending on the session.

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
