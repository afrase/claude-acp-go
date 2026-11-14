package claudeacp

import (
	"encoding/json"
	"net/url"
	"path"
	"regexp"
	"strings"

	acp "github.com/afrase/acp-go/unstable"

	"github.com/afrase/claude-acp-go/claudeagent"
)

// toClaude is a prompt's content as a claude user message's:
//
//   - text as it is (empty text is dropped: the API refuses it), but for
//     an MCP server's prompt as the client names it, /mcp:server:prompt,
//     which is claude's /server:prompt (MCP);
//   - a resource link as a Markdown link, [@name](uri), for a file;
//   - an embedded text resource as the same link, and its text in a
//     <context ref="uri"> block after everything else;
//   - an image, by its data or its http(s) URL.
//
// Audio, blobs and kinds it doesn't know are dropped: claude takes none.
func toClaude(prompt []acp.ContentBlock) ([]claudeagent.Block, error) {
	var out, context []claudeagent.Block
	for _, b := range prompt {
		switch {
		case b.Text != nil:
			if b.Text.Text != "" {
				out = append(out, text(claudePrompt(b.Text.Text)))
			}
		case b.ResourceLink != nil:
			out = append(out, text(link(b.ResourceLink.URI)))
		case b.Resource != nil:
			if r := b.Resource.Resource.TextResourceContents; r != nil {
				out = append(out, text(link(r.URI)))
				context = append(context, text("\n<context ref=\""+r.URI+"\">\n"+r.Text+"\n</context>"))
			}
		case b.Image != nil:
			if img, ok := image(b.Image); ok {
				out = append(out, img)
			}
		}
	}
	out = append(out, context...)
	if len(out) == 0 {
		return nil, invalidParams("the prompt has nothing claude takes")
	}
	return out, nil
}

// mcpPrompt is text that runs an MCP server's prompt by the name the
// client has for it: /mcp:server:prompt, then what it is given.
var mcpPrompt = regexp.MustCompile(`^/mcp:([^:\s]+):(\S+)(?:\s(.*))?$`)

// claudePrompt is text that runs an MCP server's prompt by the client's
// name for it as claude's (/server:prompt (MCP) args); other text as it
// is.
func claudePrompt(s string) string {
	m := mcpPrompt.FindStringSubmatch(s)
	if m == nil {
		return s
	}
	s = "/" + m[1] + ":" + m[2] + mcpSuffix
	if m[3] != "" {
		s += " " + m[3]
	}
	return s
}

func text(s string) claudeagent.Block { return claudeagent.Block{Type: "text", Text: s} }

// link is how a prompt mentions a resource: a file (or Zed's file URI) by
// its name, as claude's own @-mentions do; anything else as its URI.
func link(uri string) string {
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "file" && u.Scheme != "zed" {
		return uri
	}
	return "[@" + path.Base(u.Path) + "](" + uri + ")"
}

func image(img *acp.ImageContent) (claudeagent.Block, bool) {
	var source map[string]string
	switch {
	case img.Data != "":
		source = map[string]string{"type": "base64", "data": img.Data, "media_type": img.MimeType}
	case strings.HasPrefix(img.URI, "http://") || strings.HasPrefix(img.URI, "https://"):
		source = map[string]string{"type": "url", "url": img.URI}
	default:
		return claudeagent.Block{}, false
	}
	b, _ := json.Marshal(source)
	return claudeagent.Block{Type: "image", Source: b}, true
}
