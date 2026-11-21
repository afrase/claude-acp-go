package claudeagent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestArgs(t *testing.T) {
	core := []string{"--output-format", "stream-json", "--verbose", "--input-format", "stream-json"}
	value := func(s string) *string { return &s }
	allow := func(context.Context, PermissionRequest) (PermissionResult, error) { return Allow{}, nil }
	for _, tc := range []struct {
		name string
		o    Options
		want []string
	}{
		{"zero", Options{}, nil},
		{"permission prompts", Options{CanUseTool: allow}, []string{"--permission-prompt-tool=stdio"}},
		{
			"values in the equals form",
			Options{Model: "-opus", FallbackModel: "sonnet", Effort: "high", PermissionMode: PermissionDefault, Resume: "-r", SessionID: "s1",
				ForkSession: true, ResumeSessionAt: "u1", MaxTurns: 3},
			[]string{"--model=-opus", "--fallback-model=sonnet", "--effort=high", "--permission-mode=default", "--resume=-r",
				"--session-id=s1", "--fork-session", "--resume-session-at=u1", "--max-turns=3"},
		},
		{
			"lists",
			Options{SettingSources: []string{"user", "project", "local"}, AddDirs: []string{"/a", "-b"},
				AllowedTools: []string{"Read", "Bash(git:*)"}, DisallowedTools: []string{"AskUserQuestion"}, Tools: []string{"Read"}},
			[]string{"--setting-sources=user,project,local", "--add-dir=/a", "--add-dir=-b", "--allowedTools=Read,Bash(git:*)",
				"--disallowedTools=AskUserQuestion", "--tools=Read"},
		},
		{"empty lists that mean none", Options{SettingSources: []string{}, Tools: []string{}}, []string{"--setting-sources=", "--tools="}},
		{
			"JSON and the MCP config's path in an argument of their own",
			Options{Settings: `{"ultracode":true}`, MCPServers: map[string]MCPServer{"web": {Type: "http", URL: "https://x"}}},
			[]string{"--settings", `{"ultracode":true}`, "--mcp-config", "/tmp/m/mcp.json"},
		},
		{"thinking adaptive", Options{Thinking: &Thinking{Type: "adaptive", Display: "summarized"}}, []string{"--thinking=adaptive", "--thinking-display=summarized"}},
		{"thinking with a budget", Options{Thinking: &Thinking{Type: "enabled", BudgetTokens: 4000}}, []string{"--max-thinking-tokens=4000"}},
		{"thinking enabled, no budget", Options{Thinking: &Thinking{Type: "enabled"}}, []string{"--thinking=adaptive"}},
		{"thinking off", Options{Thinking: &Thinking{Type: "disabled", Display: "omitted"}}, []string{"--thinking=disabled"}},
		{
			"switches",
			Options{AllowBypass: true, IncludePartialMessages: true, ReplayUserMessages: true},
			[]string{"--allow-dangerously-skip-permissions", "--include-partial-messages", "--replay-user-messages"},
		},
		{
			"extra args, sorted",
			Options{ExtraArgs: map[string]*string{"strict-mcp-config": nil, "agent": value("-reviewer")}},
			[]string{"--agent=-reviewer", "--strict-mcp-config"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := ""
			if tc.o.MCPServers != nil {
				config = "/tmp/m/mcp.json"
			}
			got, err := args(tc.o, config)
			if err != nil {
				t.Fatal(err)
			}
			if want := append(slices.Clone(core), tc.want...); !slices.Equal(got, want) {
				t.Errorf("args:\n got %q\nwant %q", got, want)
			}
			if slices.Contains(got, "-p") || slices.Contains(got, "--print") || slices.Contains(got, "--bare") {
				t.Errorf("print mode or bare asked for: %q", got)
			}
		})
	}
}

// An MCP server goes to claude as its config was given: with no type
// when it had none (claude reads that as stdio, and refuses ""), and with
// the fields the package doesn't name.
func TestMCPServerConfig(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"stdio without a type", `{"command":"npx","args":["db-mcp"]}`, `{"command":"npx","args":["db-mcp"]}`},
		{"http with a headers helper", `{"type":"http","url":"https://x","headersHelper":"/bin/auth","oauth":{"clientId":"c"}}`,
			`{"type":"http","url":"https://x","headersHelper":"/bin/auth","oauth":{"clientId":"c"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var servers map[string]MCPServer
			if err := json.Unmarshal([]byte(`{"s":`+tc.in+`}`), &servers); err != nil {
				t.Fatal(err)
			}
			if b, err := json.Marshal(servers["s"]); err != nil || string(b) != tc.want {
				t.Errorf("%s %v", b, err)
			}
		})
	}
	if b, _ := json.Marshal(MCPServer{Command: "/bin/t"}); string(b) != `{"command":"/bin/t"}` {
		t.Errorf("built: %s", b)
	}
}

// The MCP servers' tokens are never on claude's command line, which any
// local user can read: the config is in a file only this user can read,
// in a folder of its own, removed once claude has read it.
func TestMCPConfigNotInArgs(t *testing.T) {
	servers := map[string]MCPServer{
		"gh":  {Command: "gh-mcp", Env: map[string]string{"GITHUB_TOKEN": "ghp_secret1"}},
		"web": {Type: "http", URL: "https://x", Headers: map[string]string{"Authorization": "Bearer secret2"}},
	}
	var got Spec
	tr, err := startTransport(context.Background(), Options{CLIPath: "/c", Env: []string{}, MCPServers: servers,
		Spawner: func(_ context.Context, s Spec) (*Proc, error) {
			got = s
			return &Proc{
				Stdin: nopWriteCloser{}, Stdout: io.NopCloser(strings.NewReader("")), Stderr: io.NopCloser(strings.NewReader("")),
				Process: exitedProcess{},
			}, nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	line := strings.Join(got.Args, " ")
	for _, secret := range []string{"ghp_secret1", "secret2", "GITHUB_TOKEN", "Authorization"} {
		if strings.Contains(line, secret) {
			t.Errorf("%s on the command line: %s", secret, line)
		}
	}
	i := slices.Index(got.Args, "--mcp-config")
	if i < 0 || i+1 == len(got.Args) {
		t.Fatalf("no --mcp-config: %q", got.Args)
	}
	path := got.Args[i+1]
	config, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var read struct{ MCPServers map[string]MCPServer }
	if err := json.Unmarshal(config, &read); err != nil || read.MCPServers["gh"].Env["GITHUB_TOKEN"] != "ghp_secret1" ||
		read.MCPServers["web"].Headers["Authorization"] != "Bearer secret2" {
		t.Errorf("the config: %s %v", config, err)
	}
	if runtime.GOOS != "windows" {
		for p, want := range map[string]os.FileMode{path: 0o600, filepath.Dir(path): 0o700} {
			if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != want {
				t.Errorf("%s: %v %v, want %v", p, fi.Mode().Perm(), err, want)
			}
		}
	}
	tr.configRead()
	if _, err := os.Stat(filepath.Dir(path)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the config's folder is still there: %v", err)
	}
	tr.close()

	// A start that fails leaves no config behind either.
	_, err = startTransport(context.Background(), Options{CLIPath: "/c", Env: []string{}, MCPServers: servers,
		Spawner: func(_ context.Context, s Spec) (*Proc, error) {
			got = s
			return nil, errors.ErrUnsupported
		}})
	if err == nil {
		t.Fatal("started")
	}
	if _, err := os.Stat(got.Args[slices.Index(got.Args, "--mcp-config")+1]); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a failed start left its config: %v", err)
	}
}

// A settings object, which may hold tokens, is never on claude's command
// line either: it is in a file only this user can read, in a folder of
// its own, removed once claude exits. A path is passed as it is.
func TestSettingsNotInArgs(t *testing.T) {
	const settings = `{"env":{"API_TOKEN":"tok_secret3"}}`
	var got Spec
	exit := make(chan struct{})
	tr, err := startTransport(context.Background(), Options{CLIPath: "/c", Env: []string{}, Settings: settings,
		Spawner: func(_ context.Context, s Spec) (*Proc, error) {
			got = s
			return &Proc{
				Stdin: nopWriteCloser{}, Stdout: io.NopCloser(strings.NewReader("")), Stderr: io.NopCloser(strings.NewReader("")),
				Process: exitingProcess{exit},
			}, nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	if line := strings.Join(got.Args, " "); strings.Contains(line, "tok_secret3") || strings.Contains(line, "API_TOKEN") {
		t.Errorf("the settings on the command line: %s", line)
	}
	i := slices.Index(got.Args, "--settings")
	if i < 0 || i+1 == len(got.Args) {
		t.Fatalf("no --settings: %q", got.Args)
	}
	path := got.Args[i+1]
	if b, err := os.ReadFile(path); err != nil || string(b) != settings {
		t.Errorf("the settings file: %s %v", b, err)
	}
	if runtime.GOOS != "windows" {
		for p, want := range map[string]os.FileMode{path: 0o600, filepath.Dir(path): 0o700} {
			if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != want {
				t.Errorf("%s: %v %v, want %v", p, fi.Mode().Perm(), err, want)
			}
		}
	}
	tr.configRead() // claude may read its settings again: they stay
	if _, err := os.Stat(path); err != nil {
		t.Errorf("the settings went before claude exited: %v", err)
	}
	close(exit)
	<-tr.exited
	if _, err := os.Stat(filepath.Dir(path)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the settings' folder is still there: %v", err)
	}
	tr.close()

	// A start that fails leaves no settings behind either.
	_, err = startTransport(context.Background(), Options{CLIPath: "/c", Env: []string{}, Settings: settings,
		Spawner: func(_ context.Context, s Spec) (*Proc, error) {
			got = s
			return nil, errors.ErrUnsupported
		}})
	if err == nil {
		t.Fatal("started")
	}
	if _, err := os.Stat(got.Args[slices.Index(got.Args, "--settings")+1]); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a failed start left its settings: %v", err)
	}

	// A path is claude's to read.
	_, _ = startTransport(context.Background(), Options{CLIPath: "/c", Env: []string{}, Settings: "/etc/claude.json",
		Spawner: func(_ context.Context, s Spec) (*Proc, error) {
			got = s
			return nil, errors.ErrUnsupported
		}})
	if i := slices.Index(got.Args, "--settings"); i < 0 || got.Args[i+1] != "/etc/claude.json" {
		t.Errorf("a settings path: %q", got.Args)
	}
}

// exitingProcess is a claude that exits with 0 once exit is closed.
type exitingProcess struct{ exit chan struct{} }

func (p exitingProcess) Wait() error {
	<-p.exit
	return nil
}

func (p exitingProcess) Signal(os.Signal) error {
	select {
	case <-p.exit:
		return os.ErrProcessDone
	default:
		return nil
	}
}

// exitedProcess is a claude that has exited with 0.
type exitedProcess struct{}

func (exitedProcess) Wait() error            { return nil }
func (exitedProcess) Signal(os.Signal) error { return os.ErrProcessDone }

// nopWriteCloser is a stdin that takes everything.
type nopWriteCloser struct{}

func (nopWriteCloser) Write(p []byte) (int, error) { return len(p), nil }
func (nopWriteCloser) Close() error                { return nil }

func TestArgsRefused(t *testing.T) {
	for _, o := range []Options{
		{Model: "opus", FallbackModel: "opus"},
		{ExtraArgs: map[string]*string{"bare": nil}},
		{ExtraArgs: map[string]*string{"p": nil}},
		{ExtraArgs: map[string]*string{"output-format": nil}},
		{ExtraArgs: map[string]*string{"--model": nil}},
		{ExtraArgs: map[string]*string{"a=b": nil}},
		{ExtraArgs: map[string]*string{"": nil}},
		{Settings: "-x"},
	} {
		if a, err := args(o, ""); err == nil {
			t.Errorf("%+v: %q, no error", o, a)
		}
	}
}

func TestEnviron(t *testing.T) {
	base := []string{
		"PATH=/bin", "CLAUDECODE=1", "NODE_OPTIONS=--x", "CLAUDE_AGENT_SDK_VERSION=0.3.303",
		"CLAUDE_CODE_SDK_HAS_OAUTH_REFRESH=1", "CLAUDE_CODE_SDK_HAS_HOST_AUTH_REFRESH=1",
		"CLAUDE_CODE_EMIT_SESSION_STATE_EVENTS=0", "PWD=/old", "HOME=/h",
	}
	for _, tc := range []struct {
		name string
		o    Options
		want []string
	}{
		{
			"defaults",
			Options{Env: base},
			[]string{"PATH=/bin", "PWD=/old", "HOME=/h", "CLAUDE_CODE_EMIT_SESSION_STATE_EVENTS=1",
				"CLAUDE_AGENT_SDK_CLIENT_APP=claudeagent-go/" + Version, "CLAUDE_CODE_ENTRYPOINT=sdk-cli"},
		},
		{
			"the app, the folder",
			Options{Env: base, ClientApp: "myeditor/1.2", Cwd: "/w"},
			[]string{"PATH=/bin", "HOME=/h", "CLAUDE_AGENT_SDK_CLIENT_APP=myeditor/1.2", "CLAUDE_CODE_EMIT_SESSION_STATE_EVENTS=1",
				"PWD=/w", "CLAUDE_CODE_ENTRYPOINT=sdk-cli"},
		},
		{
			"the caller's entrypoint and app are kept",
			Options{Env: []string{"CLAUDE_CODE_ENTRYPOINT=x", "CLAUDE_AGENT_SDK_CLIENT_APP=y/1"}},
			[]string{"CLAUDE_CODE_ENTRYPOINT=x", "CLAUDE_AGENT_SDK_CLIENT_APP=y/1", "CLAUDE_CODE_EMIT_SESSION_STATE_EVENTS=1"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := environ(tc.o); !slices.Equal(got, tc.want) {
				t.Errorf("environ:\n got %q\nwant %q", got, tc.want)
			}
		})
	}
}

// With no Env, claude gets this process's environment, edited.
func TestEnvironInherits(t *testing.T) {
	t.Setenv("CLAUDECODE", "1")
	t.Setenv("CLAUDEAGENT_TEST_MARK", "x")
	env := environ(Options{})
	if !slices.Contains(env, "CLAUDEAGENT_TEST_MARK=x") || slices.ContainsFunc(env, func(kv string) bool { return strings.HasPrefix(kv, "CLAUDECODE=") }) {
		t.Errorf("%q", env)
	}
}

// A Spawner gets CLIPath as it is: it may run no local claude, so none
// need be installed.
func TestSpecWithSpawnerNeedsNoClaude(t *testing.T) {
	spawn := func(context.Context, Spec) (*Proc, error) { return nil, errors.ErrUnsupported }
	for path, want := range map[string]string{"": "claude", "remote-claude": "remote-claude", "bin/claude": "bin/claude"} {
		s, err := spec(Options{CLIPath: path, Spawner: spawn, Env: []string{"PATH=" + t.TempDir()}}, "")
		if err != nil || s.Path != want {
			t.Errorf("CLIPath %q: %+v %v", path, s, err)
		}
	}
}

// claude is looked up on the PATH claude will have, not this process's.
func TestSpecFindsClaude(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "claude"), nil, 0o600); err != nil { // not executable
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "claude"), nil, 0o700); err != nil {
		t.Fatal(err)
	}
	s, err := spec(Options{Env: []string{"PATH=relative:" + dir + ":" + bin}, Cwd: "/w"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if s.Path != filepath.Join(bin, "claude") || s.Dir != "/w" || s.Args[0] != "--output-format" {
		t.Errorf("%+v", s)
	}
	if _, err := spec(Options{Env: []string{"PATH=" + dir}}, ""); err == nil {
		t.Error("found a claude that can't run")
	}
	if s, err := spec(Options{CLIPath: "/opt/claude", Env: []string{}}, ""); err != nil || s.Path != "/opt/claude" {
		t.Errorf("%+v %v", s, err)
	}
	// A relative path is this process's, never the session's folder's.
	here, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if s, err := spec(Options{CLIPath: "node_modules/.bin/claude", Cwd: "/w", Env: []string{}}, ""); err != nil || s.Path != filepath.Join(here, "node_modules", ".bin", "claude") {
		t.Errorf("%+v %v", s, err)
	}
}
