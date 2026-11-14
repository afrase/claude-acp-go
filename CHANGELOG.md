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
- `claudeacp`: a tool's result that isn't its output says why under `_meta.claudeCode`, `nonExecutionKind` (`user-rejected`, `permission-rule`, `interrupted`, `cancelled`, `automode-…`) and `userFeedback`, from claude's `tool_result_meta`, as claude-agent-acp does. `claudeagent.User` has `ToolResultMeta`, as claude sent it.
- `claudeacp`: a call claude refused without asking says the rule's reason (`decision_reason`) before claude's message, as claude-agent-acp does; its `toolResponse` leaves out fields claude didn't give, and the call's later result adds claude's text as `rawOutput`.
- `claudeacp`: a compaction's `rawOutput` says trigger `automatic` (else `manual`) as claude-agent-acp does, and leaves out counts claude didn't give, where it said `auto` and 0; a boundary with no facts gives no `rawOutput`.
- `claudeacp`: a failed `/compact` says why once, in its call: claude's copy of the error as a command's output is dropped, and a turn that showed a compaction doesn't show its result's text again, as claude-agent-acp does.
- `claudeacp`: a client that declares `session.compaction` gets ACP's `compaction_update`s for a compaction, as claude-agent-acp sends them, in place of the `Compact conversation` tool call: `in_progress`, then `completed` with the summary from claude's PostCompact hook (its `<summary>`, without the model's analysis), `failed` with the error, or `cancelled` when the turn ends first.
- `claudeacp`: a notice is shaped as claude-agent-acp shapes it: plain text (no `**`, backticks or heading marks), its first line the title and the rest the description, or a generic title when that line is over 256 characters; one per tool use, where claude repeats a tool's progress. A model fallback says `<model> declined this request (<category>); retried with <model>.` and what that means for the session, with the API's explanation as the description, where claude's own text was the whole title. `claudeagent.ModelRefusalFallback` has `Direction`, `Scope` and `APIRefusalCategory`.
- `claudeacp`: a refused turn's text is the refused response's own explanation whichever comes first, it or claude's no-fallback banner; the banner only stands in when the response gave none, and not for a turn no user started (`refused_user_message_uuid: null`), as in claude-agent-acp.
- `claudeacp`: a memory recall's read links the memory files it read, and a synthesized one (mode `synthesize`) is titled `Recalled synthesized memory` with the memory's text as its content; `_meta.claudeCode.toolResponse` says the mode, as claude-agent-acp does.
- `claudeacp`: an MCP server's prompt is offered as `mcp:server:prompt`, as claude-agent-acp names it, where claude's `server:prompt (MCP)` has a space; a prompt that starts `/mcp:server:prompt` goes to claude as `/server:prompt (MCP)`.
- `claudeacp`: a TaskList keeps each task's active form, which the listing doesn't carry, so a task under way still reads `Running tests`, not `Run tests`.
- `claudeacp`: a subagent's TaskCreate, TaskUpdate and TaskList change the session's plan too, live and in a loaded session, as the task list is the session's; they are still shown as the subagent's calls.
- `claudeagent` (API): `CompactBoundary.CompactMetadata` is a `*CompactMetadata`, nil when claude doesn't say, and its counts are pointers, nil when claude leaves them out.
- `claudeagent/claudetest`: `Replay` checks what the client decided, not only a line's kind: a control_request's plain arguments (stop_task's `task_id`, set_model's `model`; not initialize's), a control_response's subtype, the request it answers and a permission's `behavior`, and a user message's content. Ids are compared as the live ones they were mapped to; other fields aren't checked. A test that wrote other text than the recording's, or answered another way, fails now.
- `claudeagent/claudetest`: `Fake.Awaiting` is no longer met by a line the client has just written: a line on its way to the script, or taken by a step that hasn't returned, counts as given, and Awaiting waits for the script's next wait. The fake's `Proc.Stdin` is now a writer of its own that counts lines, not the bare pipe.
- `claude-tee`: a frame's `ms` is taken as the frame is written, so times never go back in a cassette when stdin and stdout lines come together.
- `claude-tee`: a cassette no longer keeps an MCP server's credentials in a `url` (its userinfo, and query parameters named like a key or token) or in its `args` (the values of flags so named, `NAME=value` pairs and `Authorization: …` style headers). A token in a URL's path is still kept: read a cassette before you share it.
- The README no longer calls `claude-acp` a static binary, and shows `CGO_ENABLED=0 go install …` for one on Linux. It and the command's doc say claude's own stderr is logged as claude wrote it (capped), so it may quote a prompt.

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
