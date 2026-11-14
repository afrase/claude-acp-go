package claudeacp

import (
	"cmp"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	acp "github.com/afrase/acp-go/unstable"

	"github.com/afrase/claude-acp-go/claudeagent"
)

// How each of claude's tools looks to a client: a title, an ACP kind,
// content and the files it touches, from its input; and what of its
// result to show. These are presentation: clients that know claude read
// the tool's name and input from rawInput and _meta.claudeCode instead.

// view is how a tool call looks, from its input.
type view struct {
	title     string
	kind      acp.ToolKind
	content   []acp.ToolCallContent
	locations []acp.ToolCallLocation
}

// toolInput is the fields of claude's tools' inputs the views read.
type toolInput struct {
	Command     string `json:"command"`
	Description string `json:"description"`
	// CommandDescription is Bash's description by another name, which
	// claude takes since 2.1.295; the streamed tool use keeps the key the
	// model wrote.
	CommandDescription string `json:"command_description"`
	FilePath           string `json:"file_path"`
	Path               string `json:"path"`
	Pattern            string `json:"pattern"`
	Offset             int    `json:"offset"`
	Limit              int    `json:"limit"`
	Content            string `json:"content"`
	// FileText and FileContent are Write's content by other names, which
	// claude takes since 2.1.280.
	FileText    string `json:"file_text"`
	FileContent string `json:"file_content"`
	OldString   string `json:"old_string"`
	NewString   string `json:"new_string"`
	URL         string `json:"url"`
	Prompt      string `json:"prompt"`
	Query       string `json:"query"`
	Plan        string `json:"plan"`
	Skill       string `json:"skill"`
	Questions   []struct {
		Question string `json:"question"`
	} `json:"questions"`
	Findings []struct {
		File    string `json:"file"`
		Line    int    `json:"line"`
		Summary string `json:"summary"`
	} `json:"findings"`
}

// toolView is how a call of the tool name with input looks; paths in
// titles are relative to cwd when inside it.
func toolView(name string, input json.RawMessage, cwd string) view {
	var in toolInput
	_ = json.Unmarshal(input, &in) // a field of another type is left out
	file := cmp.Or(in.FilePath, in.Path)
	switch name {
	case "Bash", "PowerShell":
		v := view{title: cmp.Or(in.Command, "Terminal"), kind: acp.ToolKindExecute}
		if d := cmp.Or(in.Description, in.CommandDescription); d != "" {
			v.content = []acp.ToolCallContent{textContent(d)}
		}
		return v
	case "Read":
		title := "Read " + rel(file, cwd)
		line := uint32(max(in.Offset, 1)) //nolint:gosec // a line number
		switch {
		case file == "":
			title = "Read"
		case in.Limit > 0:
			title += fmt.Sprintf(" (%d - %d)", line, int(line)+in.Limit-1)
		case in.Offset > 0:
			title += fmt.Sprintf(" (from line %d)", line)
		}
		return view{title: title, kind: acp.ToolKindRead, locations: at(file, &line)}
	case "Write":
		v := view{title: titled("Write", rel(file, cwd)), kind: acp.ToolKindEdit, locations: at(file, nil)}
		if file != "" {
			text := cmp.Or(in.Content, in.FileText, in.FileContent)
			v.content = []acp.ToolCallContent{{Diff: &acp.Diff{Path: file, NewText: text}}}
		}
		return v
	case "Edit":
		v := view{title: titled("Edit", rel(file, cwd)), kind: acp.ToolKindEdit, locations: at(file, nil)}
		if file != "" {
			v.content = []acp.ToolCallContent{{Diff: &acp.Diff{Path: file, OldText: in.OldString, NewText: in.NewString}}}
		}
		return v
	case "Glob":
		v := view{title: titled("Find", quoted(in.Pattern)), kind: acp.ToolKindSearch, locations: at(in.Path, nil)}
		if in.Path != "" {
			v.title += " in " + rel(in.Path, cwd)
		}
		return v
	case "Grep":
		v := view{title: titled("Search", quoted(in.Pattern)), kind: acp.ToolKindSearch}
		if in.Path != "" {
			v.title += " in " + rel(in.Path, cwd)
		}
		return v
	case "WebFetch":
		return view{title: titled("Fetch", in.URL), kind: acp.ToolKindFetch, content: texts(in.Prompt)}
	case "WebSearch":
		return view{title: titled("Search the web for", quoted(in.Query)), kind: acp.ToolKindFetch}
	case "Agent", "Task":
		return view{title: cmp.Or(in.Description, name), kind: acp.ToolKindThink, content: texts(in.Prompt)}
	case "ExitPlanMode":
		return view{title: "Plan ready", kind: acp.ToolKindSwitchMode, content: texts(in.Plan)}
	case "AskUserQuestion":
		v := view{title: "Asking for your input", kind: acp.ToolKindOther}
		for _, q := range in.Questions {
			v.content = append(v.content, texts(q.Question)...)
		}
		if len(in.Questions) == 1 && in.Questions[0].Question != "" {
			v.title = in.Questions[0].Question
		}
		return v
	case "Skill":
		return view{title: titled("Skill", in.Skill), kind: acp.ToolKindOther}
	case "ReportFindings":
		v := view{title: "Report findings: none found", kind: acp.ToolKindThink}
		if n := len(in.Findings); n > 0 {
			v.title = fmt.Sprintf("Report %d findings", n)
			if n == 1 {
				v.title = "Report 1 finding"
			}
		}
		for _, f := range in.Findings {
			where := f.File
			if f.Line > 0 {
				where += ":" + strconv.Itoa(f.Line)
			}
			v.content = append(v.content, textContent("**"+where+"** — "+f.Summary))
		}
		return v
	}
	return view{title: name, kind: acp.ToolKindOther} // an MCP server's tool, or one claude added since
}

// resultContent is what a tool's result shows; ok false leaves the call's
// content as it was (an edit's diff stays, and the hook's replaces it).
func resultContent(name string, b claudeagent.Block) (content []acp.ToolCallContent, ok bool) {
	text, shown := resultParts(b.Content)
	switch name {
	case "Edit", "Write":
		if !b.IsError {
			return nil, false
		}
	case "Bash", "PowerShell":
		switch {
		case shown != nil: // an image is no console output: it shows as any result's
		case text == "":
			return nil, false
		case !b.IsError: // a failed or refused command is fenced as any error, as claude-agent-acp does for a client without terminals
			return []acp.ToolCallContent{textContent(fence("console", text))}, true
		}
	case "Read":
		if !b.IsError {
			// An image or a PDF is shown as itself, after any text.
			if text = strings.Trim(reminders.ReplaceAllString(text, ""), "\n"); text != "" {
				content = []acp.ToolCallContent{textContent(fence("", text))}
			}
			content = append(content, shown...)
			return content, content != nil
		}
	case "Agent", "Task":
		text = handedBack(strings.TrimSpace(usageBlock.ReplaceAllString(text, "")))
	}
	if b.IsError && text != "" {
		text = fence("", text)
	}
	return append(texts(text), shown...), true
}

// handedBack is a foreground subagent's report out of the frame claude
// puts around it for the model, as claude-agent-acp
// shows it: without the frame's words, the indent claude gives each line,
// or what claude adds after it (the agent's id, its worktree). Any other
// text comes back as it is.
func handedBack(text string) string {
	_, report, ok := strings.Cut(text, handBackEnd)
	if !strings.HasPrefix(text, handBackStart) || !ok {
		return text
	}
	if i := strings.LastIndex(report, "\nagentId: "); i >= 0 {
		report = report[:i]
	}
	lines := strings.Split(report, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimPrefix(l, "  ")
	}
	return strings.Join(lines, "\n")
}

// resultParts reads a tool_result's content, a string, a block or
// blocks: its text (and the tools ToolSearch loaded, as claude-agent-acp
// shows them), and the rest as content to show after it: images, and a
// line for each of what can't be shown (a document, an API server tool's
// result), as claude-agent-acp shows them.
func resultParts(raw json.RawMessage) (string, []acp.ToolCallContent) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, nil
	}
	var blocks []claudeagent.Block
	if json.Unmarshal(raw, &blocks) != nil {
		var b claudeagent.Block
		if json.Unmarshal(raw, &b) != nil || b.Type == "" {
			return "", nil
		}
		blocks = []claudeagent.Block{b} // a server tool's single result
	}
	var parts []string
	var shown []acp.ToolCallContent
	for _, b := range blocks {
		switch b.Type {
		case "text":
			parts = append(parts, b.Text)
		case "tool_reference": // a tool ToolSearch loaded
			var ref struct {
				ToolName string `json:"tool_name"`
			}
			if json.Unmarshal(b.Raw, &ref) == nil && ref.ToolName != "" {
				parts = append(parts, "Tool: "+ref.ToolName)
			}
		case "image":
			var src struct {
				Type      string `json:"type"`
				Data      string `json:"data"`
				MediaType string `json:"media_type"`
				URL       string `json:"url"`
			}
			_ = json.Unmarshal(b.Source, &src)
			switch {
			case src.Data != "":
				shown = append(shown, acp.ToolCallContent{Content: &acp.Content{Content: acp.ContentBlock{Image: &acp.ImageContent{Data: src.Data, MimeType: src.MediaType}}}})
			case src.Type == "url":
				shown = append(shown, textContent("[image: "+src.URL+"]"))
			default:
				shown = append(shown, textContent("[image: file reference]"))
			}
		default:
			if line := blockLine(b); line != "" {
				shown = append(shown, textContent(line))
			}
		}
	}
	return strings.Join(parts, "\n\n"), shown
}

// blockLine is a line saying what a result's block is, for one that can't
// be shown as it is: a document (a PDF Read gives one) by its title, type
// and size, never its data; an API server tool's result by what it found;
// any other block as its JSON, as claude-agent-acp shows them.
func blockLine(b claudeagent.Block) string {
	var f struct {
		Title  string `json:"title"`
		URL    string `json:"url"`
		Source struct {
			Type      string `json:"type"`
			MediaType string `json:"media_type"`
			Data      string `json:"data"`
			URL       string `json:"url"`
		} `json:"source"`
		ErrorCode      string `json:"error_code"`
		ErrorMessage   string `json:"error_message"`
		ToolReferences []struct {
			ToolName string `json:"tool_name"`
		} `json:"tool_references"`
		Stdout       string   `json:"stdout"`
		Stderr       string   `json:"stderr"`
		IsFileUpdate bool     `json:"is_file_update"`
		Lines        []string `json:"lines"`
	}
	_ = json.Unmarshal(b.Raw, &f) // a field of another type is left out
	withMessage := func() string {
		if f.ErrorMessage != "" {
			return "Error: " + f.ErrorCode + " - " + f.ErrorMessage
		}
		return "Error: " + f.ErrorCode
	}
	switch b.Type {
	case "document":
		title := ""
		if f.Title != "" {
			title = " " + strconv.Quote(f.Title)
		}
		switch f.Source.Type {
		case "url":
			return "[document" + title + ": " + f.Source.URL + "]"
		case "base64":
			return "[document" + title + ": " + f.Source.MediaType + ", " + byteSize(len(f.Source.Data)*3/4) + "]"
		case "text":
			return "[document" + title + ": " + f.Source.MediaType + ", " + byteSize(len(f.Source.Data)) + "]"
		}
		return "[document" + title + "]"
	case "tool_search_tool_search_result":
		var names []string
		for _, r := range f.ToolReferences {
			names = append(names, r.ToolName)
		}
		return "Tools found: " + cmp.Or(strings.Join(names, ", "), "none")
	case "tool_search_tool_result_error", "text_editor_code_execution_tool_result_error":
		return withMessage()
	case "web_search_result":
		return f.Title + " (" + f.URL + ")"
	case "web_fetch_result":
		return "Fetched: " + f.URL
	case "web_search_tool_result_error", "web_fetch_tool_result_error",
		"code_execution_tool_result_error", "bash_code_execution_tool_result_error":
		return "Error: " + f.ErrorCode
	case "code_execution_result", "bash_code_execution_result":
		return "Output: " + cmp.Or(f.Stdout, f.Stderr)
	case "text_editor_code_execution_view_result":
		var v struct {
			Content string `json:"content"`
		}
		_ = json.Unmarshal(b.Raw, &v)
		return v.Content
	case "text_editor_code_execution_create_result":
		if f.IsFileUpdate {
			return "File updated"
		}
		return "File created"
	case "text_editor_code_execution_str_replace_result":
		return strings.Join(f.Lines, "\n")
	}
	return string(b.Raw)
}

// byteSize is n bytes for people to read: "312 B", "2.4 KB", "1.3 MB".
func byteSize(n int) string {
	switch {
	case n < 1024:
		return strconv.Itoa(n) + " B"
	case n < 1024*1024:
		return strconv.FormatFloat(float64(n)/1024, 'f', 1, 64) + " KB"
	}
	return strconv.FormatFloat(float64(n)/(1024*1024), 'f', 1, 64) + " MB"
}

// handBackStart and handBackEnd open a foreground subagent's report.
const (
	handBackStart = "[Subagent hand-back] "
	handBackEnd   = "The report follows:\n"
)

var (
	// reminders are the notes claude adds to a file it read, for the
	// model.
	reminders = regexp.MustCompile(`(?s)<system-reminder>.*?</system-reminder>`)
	// usageBlock is what a subagent's result ends with: its token counts,
	// for the model.
	usageBlock = regexp.MustCompile(`(?s)<usage>.*?</usage>`)
	backticks  = regexp.MustCompile("`+")
)

// fence fences text as a Markdown code block, with a fence longer than
// any run of backticks in it.
func fence(lang, text string) string {
	n := 3
	for _, run := range backticks.FindAllString(text, -1) {
		n = max(n, len(run)+1)
	}
	f := strings.Repeat("`", n)
	return f + lang + "\n" + strings.TrimRight(text, "\n") + "\n" + f
}

func textContent(s string) acp.ToolCallContent {
	return acp.ToolCallContent{Content: &acp.Content{Content: acp.ContentBlock{Text: &acp.TextContent{Text: s}}}}
}

// texts is s as content: none when it is empty.
func texts(s string) []acp.ToolCallContent {
	if s == "" {
		return nil
	}
	return []acp.ToolCallContent{textContent(s)}
}

// at is path as a call's locations: none when it is empty.
func at(path string, line *uint32) []acp.ToolCallLocation {
	if path == "" {
		return nil
	}
	return []acp.ToolCallLocation{{Path: path, Line: line}}
}

// rel is path relative to cwd when it is inside it.
func rel(path, cwd string) string {
	if r, err := filepath.Rel(cwd, path); err == nil && cwd != "" && filepath.IsAbs(path) && filepath.IsLocal(r) {
		return r
	}
	return path
}

// titled is verb and what it acts on, or the verb alone.
func titled(verb, what string) string {
	if what == "" {
		return verb
	}
	return verb + " " + what
}

func quoted(s string) string {
	if s == "" {
		return ""
	}
	return strconv.Quote(s)
}
