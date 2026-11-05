# Changelog

All notable changes to this module. It follows [Semantic Versioning](https://semver.org); until 1.0, a minor version may change the API.

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
