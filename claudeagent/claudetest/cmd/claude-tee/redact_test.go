package main

import (
	"strings"
	"testing"
)

func TestRedactJSON(t *testing.T) {
	tests := []struct {
		name, in, want string
		changed        bool
	}{
		{"mcp config", `{"mcpServers":{"gh":{"command":"gh-mcp","env":{"GH_TOKEN":"ghp_x"}},"web":{"type":"http","url":"https://x","headers":{"Authorization":"Bearer y"}}}}`,
			`{"mcpServers":{"gh":{"command":"gh-mcp","env":{"GH_TOKEN":"<redacted>"}},"web":{"headers":{"Authorization":"<redacted>"},"type":"http","url":"https://x"}}}`, true},
		{"escaped key", `{"\u0065nv":{"K":"v"},"h\u0065aders":{"A":"b"}}`, `{"env":{"K":"<redacted>"},"headers":{"A":"<redacted>"}}`, true},
		{"in an array", `[{"name":"a","env":{"K":"v"}}]`, `[{"env":{"K":"<redacted>"},"name":"a"}]`, true},
		{"settings env", `{"env":{"ANTHROPIC_API_KEY":"sk-1"},"model":"opus"}`, `{"env":{"ANTHROPIC_API_KEY":"<redacted>"},"model":"opus"}`, true},
		{"nested values", `{"env":{"K":{"deep":"v"}}}`, `{"env":{"K":"<redacted>"}}`, true},
		{"a string named env", `{"env":"prod","n":1.50}`, `{"env":"prod","n":1.50}`, false},
		{"no secrets, order kept", `{"type":"user","b":1,"a":2}`, `{"type":"user","b":1,"a":2}`, false},
		{"empty env", `{"env":{}}`, `{"env":{}}`, false},
		{"not JSON", `"env": {`, `"env": {`, false},
		{"a url's credentials", `{"mcpServers":{"z":{"type":"http","url":"https://user:pw@mcp.example.com/s/p/mcp?api_key=S1&x=1&access_token=S2"}}}`,
			`{"mcpServers":{"z":{"type":"http","url":"https://redacted@mcp.example.com/s/p/mcp?api_key=redacted&x=1&access_token=redacted"}}}`, true},
		{"a url without", `{"url":"https://mcp.example.com/mcp?x=1"}`, `{"url":"https://mcp.example.com/mcp?x=1"}`, false},
		{"args", `{"s":{"command":"x","args":["--token=S1","--api-key","S2","-v","GITHUB_PAT=S3","--header","Authorization: Bearer S4","https://h/mcp?key=S5","--port","8080","plain"]}}`,
			`{"s":{"args":["--token=<redacted>","--api-key","<redacted>","-v","GITHUB_PAT=<redacted>","--header","Authorization: <redacted>","https://h/mcp?key=redacted","--port","8080","plain"],"command":"x"}}`, true},
		{"args in a flag's value", `{"args":["--header=X-Api-Key: S1","--env=SECRET_THING=S2","--url=https://h?token=S3"]}`,
			`{"args":["--header=X-Api-Key: <redacted>","--env=SECRET_THING=<redacted>","--url=https://h?token=redacted"]}`, true},
		{"args without", `{"args":["-y","@scope/server","--port","8080","mode=fast"]}`, `{"args":["-y","@scope/server","--port","8080","mode=fast"]}`, false},
		{"redacted already", `{"url":"https://redacted@h?key=redacted","args":["--token=<redacted>","--key","<redacted>","A=<redacted>"]}`,
			`{"url":"https://redacted@h?key=redacted","args":["--token=<redacted>","--key","<redacted>","A=<redacted>"]}`, false},
		{"plain text", `hello`, `hello`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, changed := redactJSON([]byte(tt.in))
			if string(got) != tt.want || changed != tt.changed {
				t.Errorf("got %s %v\nwant %s %v", got, changed, tt.want, tt.changed)
			}
		})
	}
}

func TestRedactArgs(t *testing.T) {
	in := []string{"--mcp-config", `{"mcpServers":{"a":{"env":{"T":"s1"}}}}`, `--settings={"env":{"K":"s2"}}`, "--model", "opus", "--x=a=b", `{"env":{"T":"s3=x"}}`}
	got := strings.Join(redactArgs(in), " ")
	want := `--mcp-config {"mcpServers":{"a":{"env":{"T":"<redacted>"}}}} --settings={"env":{"K":"<redacted>"}} --model opus --x=a=b {"env":{"T":"<redacted>"}}`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	if in[1] != `{"mcpServers":{"a":{"env":{"T":"s1"}}}}` {
		t.Error("changed the caller's args")
	}
}
