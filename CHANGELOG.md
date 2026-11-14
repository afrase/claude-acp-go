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
- Docs: the package doc has an Environment section that lists what `claudeagent` removes from, sets in and defaults in claude's environment; `Options.Env` points to it.
- With `Options.Spawner` set, `CLIPath` goes to the Spawner as it is (`"claude"` when empty) instead of being looked up on PATH, so a fake or remote Spawner works where claude isn't installed.
- With `Options.Log` set, claude's version is logged from its answer to initialize; no second `claude --version` process runs per session any more, and `CLAUDEAGENT_SKIP_VERSION` is gone.
- New `claudeagent.ErrExitedBeforeInit`: `Start`'s error when claude ends before it answers initialize, whatever its exit code (with the `*ExitError` beside it for a non-zero one). `claudeacp` tells a vanished session by it rather than by the error's text.
- A write to a claude that has exited is `ErrClosed` (wrapping the pipe's error), not a bare "broken pipe"; and `Start` against a claude that exits before initialize can be written still returns `ErrExitedBeforeInit` with the `*ExitError` and its stderr.
- A session closes claude's stdout and stderr once they are read to the end, and its stdin once claude exits, instead of leaving the descriptors to the GC.
- A write with an already-ended ctx (`Send`, `SetModel`…) never reaches claude; before, it did about half the time. A request whose answer came as its ctx ended returns that answer, and isn't withdrawn.
- A write stuck on a claude that stopped reading its stdin no longer holds every other write: requests and `Send` give up as their ctx says while they wait for the write slot, so a close's bounded interrupt gets through to `Close`. Withdrawing a request waits at most 5 s to be written.
- `claudeagent.MCPServer` sends no `"type"` when `Type` is empty (claude refused `""`, so a type-less stdio server from `_meta` was skipped), and keeps a decoded config in the new `Raw`, so the fields it doesn't name (`headersHelper`, `oauth`, `timeout`…) reach claude.
- Security: `Options.MCPServers` no longer go on claude's command line, where any local user could read their env values and HTTP headers (tokens) with `ps`. They go in a 0600 file in a private 0700 temp folder, named by `--mcp-config` and removed once claude has answered initialize (or the start fails, or the session closes). A `Spawner` gets that path in `Spec.Args`; `claudetest.Fake.MCPConfig` returns what it held. A JSON `Options.Settings` still goes on the command line, as its doc now says.
- `sessions.Store.Fork` with `ForkOptions.Cwd` gives the fork's records that cwd, so `List` and `Get` report the fork in its new folder (they reported the source's, and a load from there failed).
- `sessions`: a long path's project folder under another CLI build's hash is taken only when its transcripts record that path as their cwd. The first 200 characters alone matched a sibling deep path's folder, so `List` showed, and `Fork` wrote into, another project's.
- Docs: `sessions.Store.Fork` says the subagents' transcripts aren't copied, so a replay of a fork shows its Agent calls without their subagents.

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
