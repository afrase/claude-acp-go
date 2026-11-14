package claudeacp

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	acp "github.com/afrase/acp-go/unstable"

	"github.com/afrase/claude-acp-go/claudeagent"
)

// Each tool's title, kind, content and locations, from its input; paths
// inside the session's folder are relative in titles.
func TestToolView(t *testing.T) {
	for _, tc := range []struct {
		name, input, want string
	}{
		{"Bash", `{"command":"ls -a","description":"List files"}`,
			`{"title":"ls -a","kind":"execute","content":[{"type":"content","content":{"type":"text","text":"List files"}}]}`},
		{"Bash", `{}`, `{"title":"Terminal","kind":"execute"}`},
		{"Bash", `{"command":"ls","command_description":"List"}`,
			`{"title":"ls","kind":"execute","content":[{"type":"content","content":{"type":"text","text":"List"}}]}`},
		{"Read", `{"file_path":"/w/src/a.go","offset":10,"limit":5}`,
			`{"title":"Read src/a.go (10 - 14)","kind":"read","locations":[{"path":"/w/src/a.go","line":10}]}`},
		{"Read", `{"file_path":"/elsewhere/b.go","offset":3}`,
			`{"title":"Read /elsewhere/b.go (from line 3)","kind":"read","locations":[{"path":"/elsewhere/b.go","line":3}]}`},
		{"Read", `{}`, `{"title":"Read","kind":"read"}`},
		{"Write", `{"file_path":"/w/n.txt","content":"hi"}`,
			`{"title":"Write n.txt","kind":"edit","content":[{"type":"diff","path":"/w/n.txt","newText":"hi"}],"locations":[{"path":"/w/n.txt"}]}`},
		{"Write", `{"file_path":"/w/n.txt","file_text":"hi"}`,
			`{"title":"Write n.txt","kind":"edit","content":[{"type":"diff","path":"/w/n.txt","newText":"hi"}],"locations":[{"path":"/w/n.txt"}]}`},
		{"Write", `{"file_path":"/w/n.txt","file_content":"hi"}`,
			`{"title":"Write n.txt","kind":"edit","content":[{"type":"diff","path":"/w/n.txt","newText":"hi"}],"locations":[{"path":"/w/n.txt"}]}`},
		{"Edit", `{"file_path":"/w/a.go"}`,
			`{"title":"Edit a.go","kind":"edit","content":[{"type":"diff","path":"/w/a.go","newText":""}],"locations":[{"path":"/w/a.go"}]}`},
		{"Glob", `{"pattern":"**/*.go","path":"/w/x"}`, `{"title":"Find \"**/*.go\" in x","kind":"search","locations":[{"path":"/w/x"}]}`},
		{"Grep", `{"pattern":"func main"}`, `{"title":"Search \"func main\"","kind":"search"}`},
		{"WebFetch", `{"url":"https://go.dev","prompt":"the version"}`,
			`{"title":"Fetch https://go.dev","kind":"fetch","content":[{"type":"content","content":{"type":"text","text":"the version"}}]}`},
		{"WebSearch", `{"query":"acp"}`, `{"title":"Search the web for \"acp\"","kind":"fetch"}`},
		{"Task", `{"prompt":"p"}`, `{"title":"Task","kind":"think","content":[{"type":"content","content":{"type":"text","text":"p"}}]}`},
		{"ExitPlanMode", `{"plan":"1. a"}`, `{"title":"Plan ready","kind":"switch_mode","content":[{"type":"content","content":{"type":"text","text":"1. a"}}]}`},
		{"AskUserQuestion", `{"questions":[{"question":"Which?"}]}`,
			`{"title":"Which?","kind":"other","content":[{"type":"content","content":{"type":"text","text":"Which?"}}]}`},
		{"Skill", `{"skill":"pdf"}`, `{"title":"Skill pdf","kind":"other"}`},
		{"ReportFindings", `{"level":"low"}`, `{"title":"Report findings: none found","kind":"think"}`},
		{"ReportFindings", `{"findings":[{"file":"a.go","line":3,"summary":"nil map"},{"file":"b.go","summary":"leak"}]}`,
			`{"title":"Report 2 findings","kind":"think","content":[{"type":"content","content":{"type":"text","text":"**a.go:3** — nil map"}},{"type":"content","content":{"type":"text","text":"**b.go** — leak"}}]}`},
		{"mcp__jira__search", `{"q":1}`, `{"title":"mcp__jira__search","kind":"other"}`},
		{"Read", `{"file_path":7}`, `{"title":"Read","kind":"read"}`}, // a field of another type is left out
	} {
		v := toolView(tc.name, json.RawMessage(tc.input), "/w")
		b, _ := json.Marshal(struct {
			Title     string                 `json:"title"`
			Kind      string                 `json:"kind"`
			Content   []acp.ToolCallContent  `json:"content,omitempty"`
			Locations []acp.ToolCallLocation `json:"locations,omitempty"`
		}{v.title, string(v.kind), v.content, v.locations})
		if string(b) != tc.want {
			t.Errorf("%s %s:\n%s\nwant\n%s", tc.name, tc.input, b, tc.want)
		}
	}
}

// What each tool's result shows; an edit's leaves its diff.
func TestResultContent(t *testing.T) {
	for _, tc := range []struct {
		name, result string
		isError      bool
		want         string // "-" leaves the content
	}{
		{"Bash", `"a\nb"`, false, "```console\na\nb\n```"},
		{"Bash", `""`, false, "-"},
		{"Bash", `"User refused permission to run tool"`, true, "```\nUser refused permission to run tool\n```"},
		{"PowerShell", `"Exit code 1\nFAIL"`, true, "```\nExit code 1\nFAIL\n```"},
		{"Read", `"     1\tx = 1\n<system-reminder>be careful</system-reminder>\n"`, false, "```\n     1\tx = 1\n```"},
		{"Read", `"no such file"`, true, "```\nno such file\n```"},
		{"Edit", `"ok"`, false, "-"},
		{"Write", `"denied"`, true, "```\ndenied\n```"},
		{"Agent", `[{"type":"text","text":"Done."},{"type":"text","text":"agentId: a1\n<usage>x</usage>"}]`, false, "Done.\n\nagentId: a1"},
		{"Agent", `[{"type":"text","text":"[Subagent hand-back] Its frame. The report follows:\n  Line one.\n    Line two.\nagentId: a1\nworktreePath: /r/w\n<usage>x</usage>"}]`, false, "Line one.\n  Line two."},
		{"Agent", `"Says [Subagent hand-back] The report follows:\nno frame"`, false, "Says [Subagent hand-back] The report follows:\nno frame"},
		{"mcp__s__t", `[{"type":"text","text":"has ` + "```" + ` in it"}]`, true, "````\nhas ``` in it\n````"},
		{"ToolSearch", `[{"type":"tool_reference","tool_name":"SendMessage"},{"type":"tool_reference","tool_name":"TaskStop"}]`, false, "Tool: SendMessage\n\nTool: TaskStop"},
		{"mcp__s__t", `[{"type":"image","source":{"type":"base64","data":"AAA","media_type":"image/png"}}]`, false, "image image/png"},
		// A Read of an image or a PDF shows the image, or a line for the
		// document; a command's image output shows too.
		{"Read", `[{"type":"image","source":{"type":"base64","data":"AAA","media_type":"image/png"}}]`, false, "image image/png"},
		{"Read", `[{"type":"text","text":"PDF file read"},{"type":"document","title":"Spec","source":{"type":"base64","media_type":"application/pdf","data":"` + strings.Repeat("A", 2800) + `"}}]`, false,
			"```\nPDF file read\n```" + ` + [document "Spec": application/pdf, 2.1 KB]`},
		{"Read", `[{"type":"document","source":{"type":"url","url":"https://x/a.pdf"}}]`, false, "[document: https://x/a.pdf]"},
		{"Bash", `[{"type":"image","source":{"type":"base64","data":"AAA","media_type":"image/png"}}]`, false, "image image/png"},
		{"Bash", `[{"type":"text","text":"hi"},{"type":"image","source":{"type":"base64","data":"AAA","media_type":"image/png"}}]`, false, "hi + image image/png"},
		{"mcp__s__t", `[{"type":"image","source":{"type":"url","url":"https://x/a.png"}}]`, false, "[image: https://x/a.png]"},
		// An API server tool's result says what it found.
		{"web_search", `[{"type":"web_search_result","title":"Go","url":"https://go.dev"},{"type":"web_search_result","title":"ACP","url":"https://acp.dev"}]`, false,
			"Go (https://go.dev) + ACP (https://acp.dev)"},
		{"web_search", `{"type":"web_search_tool_result_error","error_code":"max_uses_exceeded"}`, false, "Error: max_uses_exceeded"},
		{"web_fetch", `{"type":"web_fetch_result","url":"https://go.dev","content":{"type":"document"}}`, false, "Fetched: https://go.dev"},
		{"tool_search_tool_regex", `{"type":"tool_search_tool_search_result","tool_references":[{"type":"tool_reference","tool_name":"x"},{"type":"tool_reference","tool_name":"y"}]}`, false,
			"Tools found: x, y"},
		{"tool_search_tool_regex", `{"type":"tool_search_tool_result_error","error_code":"unavailable","error_message":"down"}`, false, "Error: unavailable - down"},
		{"code_execution", `{"type":"bash_code_execution_result","stdout":"","stderr":"oops","return_code":1}`, false, "Output: oops"},
	} {
		content, ok := resultContent(tc.name, claudeagent.Block{Content: json.RawMessage(tc.result), IsError: tc.isError}, nil)
		got := "-"
		if ok {
			var parts []string
			for _, c := range content {
				switch {
				case c.Content == nil:
					parts = append(parts, "?")
				case c.Content.Content.Text != nil:
					parts = append(parts, c.Content.Content.Text.Text)
				case c.Content.Content.Image != nil:
					parts = append(parts, "image "+c.Content.Content.Image.MimeType)
				default:
					parts = append(parts, "?")
				}
			}
			got = strings.Join(parts, " + ")
		}
		if got != tc.want {
			t.Errorf("%s %s: %q, want %q", tc.name, tc.result, got, tc.want)
		}
	}
}

// A result shows the tool's own output where claude gave it, as
// claude-agent-acp does: a command's output without claude's wrapper for
// the model, a file without its reminders, the hits of a web search and a
// subagent's report; else the text claude gave the model.
func TestResultFromStructured(t *testing.T) {
	persisted := `"<persisted-output>\nOutput too large (40KB). Full output saved to: /tmp/x.txt\n\nPreview (first 2KB):\nabc\n</persisted-output>"`
	for _, tc := range []struct {
		name, result, structured string
		isError                  bool
		want                     string // "-" leaves the content
	}{
		{"Bash", persisted, `{"stdout":"abc","stderr":"","interrupted":false,"persistedOutputPath":"/tmp/x.txt","persistedOutputSize":40960}`, false,
			"```console\nabc\n[Output truncated (40960 bytes total): full output saved to /tmp/x.txt]\n```"},
		{"Bash", `"out\nerr\n(hint for the model)"`, `{"stdout":"out\n","stderr":"err","interrupted":true}`, false,
			"```console\nout\n\nerr\n[Command was aborted before completion]\n```"},
		{"Bash", `""`, `{"stdout":"","stderr":"","interrupted":false}`, false, "-"},
		{"Bash", `"Command running in background with ID: b1"`, `{"stdout":"","stderr":"","backgroundTaskId":"b1"}`, false,
			"```console\nCommand running in background with ID: b1\n```"},
		{"Bash", `"Exit code 1\nboom"`, `{"stdout":"","stderr":"boom"}`, true, "```\nExit code 1\nboom\n```"}, // a failure shows claude's text
		{"Read", `"     5\ta\n     6\tb\n<system-reminder>r</system-reminder>"`, `{"type":"text","file":{"filePath":"/w/a","content":"a\nb\n","startLine":5,"numLines":2,"totalLines":9}}`, false,
			"```\n5\ta\n6\tb\n```"},
		{"Read", `"     1\ta\n[truncated]"`, `{"type":"text","file":{"content":"a","startLine":1,"numLines":1,"totalLines":3,"truncatedByTokenCap":true}}`, false,
			"```\n1\ta\n[File truncated: showing 1 of 3 lines]\n```"},
		{"Read", `"x"`, `{"type":"text","file":{"content":"","startLine":1}}`, false, "```\nx\n```"}, // an empty file keeps claude's note
		{"WebSearch", `"Web search results for query: \"acp\"\n\nLinks: [{\"title\":\"A\",\"url\":\"https://a\"}]"`,
			`{"query":"acp","results":[{"tool_use_id":"s1","content":[{"title":"A","url":"https://a"},{"title":"B","url":"https://b"}]},"A summary.",{"content":[{"title":7}]}]}`, false,
			"A (https://a)\nB (https://b)\nA summary."},
		{"WebSearch", `"raw"`, `{"results":[]}`, false, "raw"},
		{"Agent", `[{"type":"text","text":"[Subagent hand-back] Frame. The report follows:\n  Done.\nagentId: a1 (use SendMessage to continue)"}]`,
			`{"status":"completed","agentId":"a1","content":[{"type":"text","text":"Done."}]}`, false, "Done."},
		{"Agent", `[{"type":"text","text":"Raw."}]`, `{"status":"completed","content":[]}`, false, "Raw."},
		{"Bash", `"a"`, `[1]`, false, "```console\na\n```"}, // not an object: the text
	} {
		content, ok := resultContent(tc.name, claudeagent.Block{Content: json.RawMessage(tc.result), IsError: tc.isError}, json.RawMessage(tc.structured))
		got := "-"
		if ok {
			got = "?"
			if len(content) == 1 && content[0].Content != nil && content[0].Content.Content.Text != nil {
				got = content[0].Content.Content.Text.Text
			}
		}
		if got != tc.want {
			t.Errorf("%s %s: %q, want %q", tc.name, tc.structured, got, tc.want)
		}
	}
}

// A subagent's report shows without claude's frame and trailer wherever
// they are: notes above the frame lead the report, a turn limit's as a
// label; an unframed report loses its agentId line; only a trailing
// <usage> block goes.
func TestHandedBack(t *testing.T) {
	frame := "[Subagent hand-back] The text below is the final report of a subagent. The report follows:\n"
	for _, tc := range []struct{ in, want string }{
		{"  NOTE: this agent stopped at its 5-turn limit before finishing. Send it a message (SendMessage) to go on.\n" + frame +
			"  Line one.\n\n  Line two.\nagentId: a1 (use SendMessage to continue)\n<usage>tokens: 5</usage>",
			"[Agent stopped at its turn limit — the output below is partial]\n\nLine one.\n\nLine two."},
		{"  Output saved to /tmp/x\n" + frame + "  Line one.\nagentId: a1 (use SendMessage to continue)", "Output saved to /tmp/x\n\nLine one."},
		{"Done.\nagentId: a1 (use SendMessage with to: 'a1' to continue this agent)", "Done."},
		{"Done.\nagentId: a1\n<usage>x</usage>", "Done.\nagentId: a1"}, // no "(…)": not claude's line
		{"Use <usage>a</usage> tags.\n<usage>tokens: 5</usage>", "Use <usage>a</usage> tags."},
		{"NOTE: this agent stopped at its 3-turn limit before finishing.", "[Agent stopped at its turn limit — the output below is partial]"},
		{"Says [Subagent hand-back] The report follows:\nno frame", "Says [Subagent hand-back] The report follows:\nno frame"},
	} {
		content, _ := resultContent("Agent", claudeagent.Block{Content: json.RawMessage(strconv.Quote(tc.in))}, nil)
		if len(content) != 1 || content[0].Content.Content.Text.Text != tc.want {
			b, _ := json.Marshal(content)
			t.Errorf("%q:\n%s\nwant %q", tc.in, b, tc.want)
		}
	}
	content, _ := resultContent("Agent", claudeagent.Block{Content: json.RawMessage(`"raw"`)},
		json.RawMessage(`{"status":"completed","content":[{"type":"text","text":"NOTE: this agent stopped at its 5-turn limit before finishing. Go on.\n\nPartial."}]}`))
	if len(content) != 1 || content[0].Content.Content.Text.Text != "[Agent stopped at its turn limit — the output below is partial]\n\nPartial." {
		b, _ := json.Marshal(content)
		t.Errorf("%s", b)
	}
}
