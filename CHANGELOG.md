# Changelog

All notable changes to this module. It follows [Semantic Versioning](https://semver.org); until 1.0, a minor version may change the API.

## Unreleased

- The module builds on Windows (`claudeagent/sessions` used Unix-only open flags; claude-tee Unix-only signals). `make check` and CI build and vet it for Windows; the tests still run on macOS and Linux only.
- `claudeagent.Decode` copies the line it is given, so a message's `Raw` no longer changes when the caller reuses its buffer (a `bufio.Scanner`'s, say).
- `claudeagent.ContextCategory` has `Kind` ("used", "free", "buffer", "deferred"), the field claude says to classify a category on, rather than its English name.
- API change: `Session.CancelAsyncMessage` returns `(cancelled bool, err error)`. `cancelled` is false when claude had already taken the message (or folded it into a batch) and will act on it; before, nil said nothing about that.
- `CanUseTool` may return `&Allow{…}` or `&Deny{…}`: a pointer was refused at run time as "no Allow or Deny", so the tool was denied.
- An `Allow`'s `PermissionUpdate` built without `Destination` is sent with `DestinationSession`, and one that lacks what its type uses (`Rules` and `Behavior`, `Mode`, `Directories`) is refused with an error that names it: claude dropped such an update silently, so an "always allow" was never kept.
- `HookFunc`'s doc says hooks fail open: an error or a missed deadline lets the guarded action go ahead. New `HookMatcher.OnFailure` (`HookFailContinue`, `HookFailBlock`) is sent as claude's `onFailure`, so an erroring guard hook can block instead.

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
