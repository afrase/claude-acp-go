package claudeacp

import (
	"errors"
	"strings"
	"testing"

	acp "github.com/afrase/acp-go/unstable"

	"github.com/afrase/claude-acp-go/claudeagent/claudetest"
)

// A change claude answers with an error is an internal error that says
// what didn't change and why, and the session's options stay as they
// were.
func TestSetConfigRefusedByClaude(t *testing.T) {
	refuse := `{"type":"control_response","response":{"subtype":"error","request_id":"$req","error":"not now"}}`
	f := started(initAnswer, `null`)
	for _, subtype := range []string{"set_permission_mode", "set_model", "apply_flag_settings"} {
		f.Respond(`{"type":"control_request","request":{"subtype":"`+subtype+`"}}`, refuse)
	}
	p := start(t, nil, f)
	r := p.openWith(t, nil)
	before := summary(r.ConfigOptions)
	for _, tc := range []struct{ id, value, what string }{
		{"mode", "plan", "the mode"},
		{"model", "sonnet", "the model"},
		{"effort", "high", "the effort"},
		{"fast", "on", "fast mode"},
	} {
		_, err := p.SetSessionConfigOption(t.Context(), &acp.SetSessionConfigOptionRequest{SessionID: r.SessionID, ConfigID: acp.SessionConfigID(tc.id),
			ValueID: &acp.SetSessionConfigOptionRequestValueID{Value: acp.SessionConfigValueID(tc.value)}})
		ae := (*acp.Error)(nil)
		if !errors.As(err, &ae) || ae.Code != acp.ErrorCodeInternalError || !strings.HasPrefix(ae.Message, "Claude didn't change "+tc.what+": ") || !strings.HasSuffix(ae.Message, ": not now") {
			t.Errorf("%s=%s: %v", tc.id, tc.value, err)
		}
	}
	if _, err := p.SetSessionMode(t.Context(), &acp.SetSessionModeRequest{SessionID: r.SessionID, ModeID: "plan"}); err == nil || !strings.Contains(err.Error(), "Claude didn't change the mode: ") || !strings.Contains(err.Error(), "not now") {
		t.Errorf("set_mode: %v", err)
	}
	if got := len(requests(t, f)); got != 5 {
		t.Errorf("claude was asked %d times, want 5", got)
	}
	if ms := p.of("current_mode_update"); len(ms) != 0 {
		t.Errorf("a mode the change didn't make: %+v", ms)
	}
	r2, err := p.SetSessionConfigOption(t.Context(), &acp.SetSessionConfigOptionRequest{SessionID: r.SessionID, ConfigID: "agent",
		ValueID: &acp.SetSessionConfigOptionRequestValueID{Value: "default"}}) // the current value: asks claude nothing
	if err != nil {
		t.Fatal(err)
	}
	if got := summary(r2.ConfigOptions); got != before {
		t.Errorf("options\n%s\nwant\n%s", got, before)
	}
}

// A change claude never answers, because it exits first, says the
// session has ended.
func TestSetConfigClaudeGone(t *testing.T) {
	for _, tc := range []struct{ id, value, subtype string }{
		{"mode", "plan", "set_permission_mode"},
		{"model", "sonnet", "set_model"},
		{"effort", "high", "apply_flag_settings"},
	} {
		t.Run(tc.id, func(t *testing.T) {
			f := started(initAnswer, `null`, claudetest.Expect(`{"type":"control_request","request":{"subtype":"`+tc.subtype+`"}}`), claudetest.Exit(1, ""))
			p := start(t, nil, f)
			sid := p.openWith(t, nil).SessionID
			_, err := p.SetSessionConfigOption(t.Context(), &acp.SetSessionConfigOptionRequest{SessionID: sid, ConfigID: acp.SessionConfigID(tc.id),
				ValueID: &acp.SetSessionConfigOptionRequestValueID{Value: acp.SessionConfigValueID(tc.value)}})
			ae := (*acp.Error)(nil)
			if !errors.As(err, &ae) || ae.Code != errEnded.Code || ae.Message != errEnded.Message {
				t.Errorf("%v, want %v", err, errEnded)
			}
		})
	}
}
