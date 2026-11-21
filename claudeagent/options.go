package claudeagent

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
)

// PermissionMode is how claude asks before it uses a tool.
type PermissionMode string

// The permission modes claude knows; it may know more.
const (
	PermissionDefault     PermissionMode = "default" // ask, as the settings say ("Manual")
	PermissionAcceptEdits PermissionMode = "acceptEdits"
	PermissionPlan        PermissionMode = "plan"
	PermissionAuto        PermissionMode = "auto"
	PermissionBypass      PermissionMode = "bypassPermissions"
	PermissionDontAsk     PermissionMode = "dontAsk"
)

// Thinking is how the model thinks before it answers.
type Thinking struct {
	Type         ThinkingType
	BudgetTokens int    // for ThinkingEnabled; 0 means adaptive
	Display      string // "summarized", "omitted"…; "" = claude's default
}

// ThinkingType is whether the model thinks, and how much.
type ThinkingType string

// The thinking types.
const (
	ThinkingAdaptive ThinkingType = "adaptive" // as much as the model decides
	ThinkingEnabled  ThinkingType = "enabled"  // up to BudgetTokens
	ThinkingDisabled ThinkingType = "disabled"
)

// MCPServer is an MCP server claude starts or connects to for the session.
type MCPServer struct {
	Type    string            `json:"type,omitempty"` // "stdio" (also ""), "http" or "sse"
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	// Raw is the server's config as decoded. When set it is what claude
	// gets, unchanged, with the fields the package doesn't name (timeout,
	// headersHelper, oauth…); clear it to send the fields instead.
	Raw json.RawMessage `json:"-"`
}

// UnmarshalJSON decodes m, keeping it in Raw.
func (m *MCPServer) UnmarshalJSON(data []byte) error {
	type plain MCPServer
	if err := json.Unmarshal(data, (*plain)(m)); err != nil {
		return err
	}
	m.Raw = bytes.Clone(data)
	return nil
}

// MarshalJSON encodes m: Raw when set, else the fields.
func (m MCPServer) MarshalJSON() ([]byte, error) {
	if len(m.Raw) > 0 {
		return m.Raw, nil
	}
	type plain MCPServer
	return json.Marshal(plain(m))
}

// Options says how to run claude. The zero value runs the claude on PATH
// in the current folder with claude's own defaults.
type Options struct {
	CLIPath string   // "" = claude, looked up on Env's PATH; with a Spawner, passed to it as it is
	Cwd     string   // "" = this process's
	Env     []string // nil = os.Environ(); edited as the package doc's Environment says

	// Model is the model to start with, an alias ("opus") or an id; "" =
	// claude's default. SetModel switches it later.
	Model string
	// FallbackModel is the model claude retries on when Model is
	// overloaded or refuses; it can't be Model.
	FallbackModel string
	// Effort is how hard the model works ("low", "medium", "high"…; a
	// model's SupportedEffortLevels); "" = claude's default.
	Effort          string
	PermissionMode  PermissionMode // "" = claude decides, from the settings
	AllowBypass     bool           // lets the session switch to PermissionBypass
	Resume          string         // a session id to continue
	SessionID       string         // the new session's id
	ForkSession     bool           // with Resume: continue in a new session
	ResumeSessionAt string         // with Resume: the message to resume after
	SettingSources  []string       // nil = every source; empty = none
	// Settings is --settings: a JSON object, or a settings file's path.
	// An object, which may hold secrets (an env block's tokens), is
	// passed in a file only this user can read, removed once claude
	// exits, never on claude's command line.
	Settings string
	// MCPServers are passed in a file only this user can read, removed
	// once claude has answered initialize, never on claude's command line.
	MCPServers             map[string]MCPServer
	AddDirs                []string
	AllowedTools           []string
	DisallowedTools        []string
	Tools                  []string // nil = the default set; empty = none
	Thinking               *Thinking
	MaxTurns               int
	IncludePartialMessages bool // StreamEvent messages as the model writes
	ReplayUserMessages     bool // claude echoes the user messages it takes
	// ExtraArgs passes flags the options don't name: --key=value, or
	// --key for a nil value.
	ExtraArgs map[string]*string

	// Init is what the initialize request carries.
	Init Init

	// The callbacks answer claude's requests. Each runs in a goroutine
	// of its own; its ctx ends when claude withdraws the request or the
	// session closes (a hook's also at the call's deadline), and then
	// what it returns isn't sent.

	// CanUseTool answers claude's permission requests; without it claude
	// asks no one and decides by its settings. Nothing times out: until
	// it returns, the tool waits.
	CanUseTool func(ctx context.Context, r PermissionRequest) (PermissionResult, error)
	// Hooks run at points in a turn, by event, beside the hooks in
	// claude's settings.
	Hooks map[HookEvent][]HookMatcher
	// OnElicitation answers MCP servers' requests for the user's input;
	// without it each is declined.
	OnElicitation func(ctx context.Context, r ElicitationRequest) (ElicitationResult, error)
	// OnControl answers the requests the package doesn't, by subtype;
	// its result is the answer's payload. ErrUnsupported, or no
	// OnControl, tells claude the subtype isn't supported. Requests for
	// credentials never reach it.
	OnControl func(ctx context.Context, subtype string, req json.RawMessage) (any, error)

	// Stderr takes each line claude writes on stderr; nil = the log. It
	// runs on the reading goroutine, and claude's exit is reported only
	// once stderr is read through, so it should return promptly.
	Stderr    func(line string)
	ClientApp string       // names the app in claude's User-Agent, e.g. "myeditor/1.2"
	MaxLine   int          // the longest stdout line read; 0 = 128 MiB
	Spawner   Spawner      // nil = Exec; Spec.Path is then CLIPath unresolved ("claude" when "")
	Log       *slog.Logger // nil = no log
	// SharedGroup keeps claude in this process's group rather than one of
	// its own (see Exec), for a program run by a supervisor that stops it
	// by killing its group, as an ACP client stops its agent: claude can't
	// outlive it then. Close's signals reach claude alone.
	SharedGroup bool
}

// defaultMaxLine is the stdout line cap: a tool result with images can
// run to tens of MiB, and a line over the cap is lost.
const defaultMaxLine = 128 << 20

// args is claude's command line (after its name) for o, with mcpConfig
// the path of the MCP servers' config file, if any (writeMCPConfig).
// Values go in the --flag=value form, so one starting with "-" can't be
// read as a flag; JSON and the config's path go in an argument of their
// own, as the official SDKs pass them. There is no -p: claude doesn't run
// interactively when its stdout isn't a terminal, and the SDKs rely on
// that too.
func args(o Options, mcpConfig string) ([]string, error) {
	if o.FallbackModel != "" && o.FallbackModel == o.Model {
		return nil, errors.New("claudeagent: the fallback model is the model")
	}
	a := []string{"--output-format", "stream-json", "--verbose", "--input-format", "stream-json"}
	if o.CanUseTool != nil {
		a = append(a, "--permission-prompt-tool=stdio") // asks come to us as can_use_tool requests
	}
	flag := func(name, value string) {
		if value != "" {
			a = append(a, "--"+name+"="+value)
		}
	}
	on := func(name string, set bool) {
		if set {
			a = append(a, "--"+name)
		}
	}
	list := func(name string, values []string) {
		if values != nil {
			a = append(a, "--"+name+"="+strings.Join(values, ","))
		}
	}
	flag("model", o.Model)
	flag("fallback-model", o.FallbackModel)
	flag("effort", o.Effort)
	flag("permission-mode", string(o.PermissionMode))
	on("allow-dangerously-skip-permissions", o.AllowBypass)
	flag("resume", o.Resume)
	flag("session-id", o.SessionID)
	on("fork-session", o.ForkSession)
	flag("resume-session-at", o.ResumeSessionAt)
	list("setting-sources", o.SettingSources)
	if strings.HasPrefix(o.Settings, "-") {
		return nil, errors.New("claudeagent: Settings is a JSON object or a file's path, not a flag")
	} else if o.Settings != "" {
		a = append(a, "--settings", o.Settings)
	}
	if mcpConfig != "" {
		a = append(a, "--mcp-config", mcpConfig)
	}
	for _, d := range o.AddDirs {
		flag("add-dir", d)
	}
	if len(o.AllowedTools) > 0 {
		list("allowedTools", o.AllowedTools)
	}
	if len(o.DisallowedTools) > 0 {
		list("disallowedTools", o.DisallowedTools)
	}
	list("tools", o.Tools)
	if t := o.Thinking; t != nil {
		switch {
		case t.Type == ThinkingDisabled:
			a = append(a, "--thinking=disabled")
		case t.Type == ThinkingEnabled && t.BudgetTokens > 0:
			a = append(a, "--max-thinking-tokens="+strconv.Itoa(t.BudgetTokens))
		default:
			a = append(a, "--thinking=adaptive")
		}
		if t.Type != ThinkingDisabled {
			flag("thinking-display", t.Display)
		}
	}
	if o.MaxTurns > 0 {
		flag("max-turns", strconv.Itoa(o.MaxTurns))
	}
	on("include-partial-messages", o.IncludePartialMessages)
	on("replay-user-messages", o.ReplayUserMessages)
	for _, k := range slices.Sorted(maps.Keys(o.ExtraArgs)) {
		if err := extraArg(k); err != nil {
			return nil, err
		}
		if v := o.ExtraArgs[k]; v != nil {
			a = append(a, "--"+k+"="+*v)
		} else {
			a = append(a, "--"+k)
		}
	}
	return a, nil
}

// extraArg refuses a key that isn't a flag's name, and the flags that
// would change what the package relies on: print mode's own, and --bare,
// which would drop the user's login, CLAUDE.md, hooks and skills.
func extraArg(k string) error {
	switch {
	case k == "" || strings.HasPrefix(k, "-") || strings.ContainsAny(k, "= \t\n"):
		return fmt.Errorf("claudeagent: %q isn't a flag's name", k)
	case k == "bare" || k == "print" || k == "p" || k == "input-format" || k == "output-format":
		return fmt.Errorf("claudeagent: --%s can't be set: the package sets how claude runs", k)
	}
	return nil
}

// environ is claude's environment for o, edited as the package doc's
// Environment section says. CLAUDE_CODE_ENTRYPOINT's sdk-cli is claude's
// own value for a run in print mode with no official SDK.
func environ(o Options) []string {
	base := o.Env
	if base == nil {
		base = os.Environ()
	}
	drop := map[string]bool{
		"CLAUDECODE": true, "NODE_OPTIONS": true, "CLAUDE_AGENT_SDK_VERSION": true,
		"CLAUDE_CODE_SDK_HAS_OAUTH_REFRESH": true, "CLAUDE_CODE_SDK_HAS_HOST_AUTH_REFRESH": true,
	}
	set := map[string]string{"CLAUDE_CODE_EMIT_SESSION_STATE_EVENTS": "1"}
	keep := map[string]string{ // unless the environment has them
		"CLAUDE_CODE_ENTRYPOINT":      "sdk-cli",
		"CLAUDE_AGENT_SDK_CLIENT_APP": "claudeagent-go/" + Version,
	}
	if o.ClientApp != "" {
		set["CLAUDE_AGENT_SDK_CLIENT_APP"] = o.ClientApp
		delete(keep, "CLAUDE_AGENT_SDK_CLIENT_APP")
	}
	if o.Cwd != "" {
		set["PWD"] = o.Cwd
	}
	env := make([]string, 0, len(base)+len(set)+len(keep))
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		if drop[k] {
			continue
		}
		if _, ok := set[k]; ok {
			continue
		}
		delete(keep, k)
		env = append(env, kv)
	}
	for _, m := range []map[string]string{set, keep} {
		for _, k := range slices.Sorted(maps.Keys(m)) {
			env = append(env, k+"="+m[k])
		}
	}
	return env
}

// writeMCPConfig writes the MCP servers' config, for claude's
// --mcp-config, to a file only this user can read, in a new folder of its
// own, and returns its path and a func that removes both; "" when there
// are no servers. A file, not an argument: the servers' env and headers
// carry tokens, and any local user can read a process's arguments (ps,
// /proc/<pid>/cmdline). claude reads the file as it starts.
func writeMCPConfig(servers map[string]MCPServer) (path string, remove func(), err error) {
	if len(servers) == 0 {
		return "", func() {}, nil
	}
	config, err := json.Marshal(map[string]any{"mcpServers": servers})
	if err != nil {
		return "", nil, fmt.Errorf("claudeagent: MCP servers: %w", err)
	}
	dir, err := os.MkdirTemp("", "claudeagent-mcp-") // 0700
	if err != nil {
		return "", nil, fmt.Errorf("claudeagent: a folder for the MCP config: %w", err)
	}
	remove = func() { _ = os.RemoveAll(dir) }
	path = filepath.Join(dir, "mcp.json")
	if err := os.WriteFile(path, config, 0o600); err != nil {
		remove()
		return "", nil, fmt.Errorf("claudeagent: writing the MCP config: %w", err)
	}
	return path, remove, nil
}

// writeSettings writes a JSON object setting, for claude's --settings,
// to a file only this user can read, in a new folder of its own, and
// returns its path and a func that removes both; a path setting, or none,
// is returned as it is. A file, not an argument, as writeMCPConfig says;
// it stays until claude exits, which may read it again.
func writeSettings(settings string) (arg string, remove func(), err error) {
	if !strings.HasPrefix(strings.TrimSpace(settings), "{") {
		return settings, func() {}, nil
	}
	dir, err := os.MkdirTemp("", "claudeagent-settings-") // 0700
	if err != nil {
		return "", nil, fmt.Errorf("claudeagent: a folder for the settings: %w", err)
	}
	remove = func() { _ = os.RemoveAll(dir) }
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte(settings), 0o600); err != nil {
		remove()
		return "", nil, fmt.Errorf("claudeagent: writing the settings: %w", err)
	}
	return path, remove, nil
}

// spec is how to start claude for o; mcpConfig is as args takes it.
func spec(o Options, mcpConfig string) (Spec, error) {
	a, err := args(o, mcpConfig)
	if err != nil {
		return Spec{}, err
	}
	env := environ(o)
	path := o.CLIPath
	switch {
	case o.Spawner != nil:
		// A Spawner may run no local program at all (a fake, a
		// container): the name is its to resolve.
		path = cmp.Or(path, "claude")
	case path == "":
		path = "claude"
		fallthrough
	case !strings.ContainsAny(path, "/"+string(filepath.Separator)): // Windows takes either
		if path, err = lookPath(path, getenv(env, "PATH"), runtime.GOOS == "windows", getenv(env, "PATHEXT")); err != nil {
			return Spec{}, err
		}
	case !filepath.IsAbs(path):
		// Against this process's folder: claude runs in Cwd, where a
		// relative path would name whatever the project put there.
		if path, err = filepath.Abs(path); err != nil {
			return Spec{}, fmt.Errorf("claudeagent: CLIPath %q: %w", o.CLIPath, err)
		}
	}
	return Spec{Path: path, Args: a, Env: env, Dir: o.Cwd, SharedGroup: o.SharedGroup}, nil
}

// lookPath finds the program name on path, a PATH list: exec.LookPath
// reads this process's PATH, and claude's environment may have another.
// On Windows (windows), where files have no execute bits, a program is
// a file with an extension in pathext (PATHEXT), tried in its order when
// name has none of them.
func lookPath(name, path string, windows bool, pathext string) (string, error) {
	names := []string{name}
	if windows {
		exts := strings.Split(strings.ToLower(cmp.Or(pathext, ".com;.exe;.bat;.cmd")), ";")
		if !slices.Contains(exts, strings.ToLower(filepath.Ext(name))) {
			names = nil
			for _, ext := range exts {
				if ext != "" {
					names = append(names, name+ext)
				}
			}
		}
	}
	for _, dir := range filepath.SplitList(path) {
		if dir == "" || !filepath.IsAbs(dir) {
			continue // a relative PATH entry would resolve against our folder, not claude's
		}
		for _, n := range names {
			p := filepath.Join(dir, n)
			if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() && (windows || fi.Mode()&0o111 != 0) {
				return p, nil
			}
		}
	}
	return "", fmt.Errorf("claudeagent: %s isn't on PATH", name)
}

// getenv is key's value in env, the last one set.
func getenv(env []string, key string) string {
	for _, kv := range slices.Backward(env) {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			return v
		}
	}
	return ""
}
