package claudeacp

import (
	"context"
	"slices"
	"strings"
	"testing"

	acp "github.com/afrase/acp-go/unstable"

	"github.com/afrase/claude-acp-go/claudeagent/claudetest"
)

// modeUpdates are the modes of the current_mode_updates so far.
func (c *client) modeUpdates() []string {
	var out []string
	for _, u := range c.of("current_mode_update") {
		out = append(out, string(u.CurrentModeUpdate.CurrentModeID))
	}
	return out
}

// toolDone is toolu_1's result.
var toolDone = sendf(`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"ok"}]}}`)

// optionIDs are a permission request's options' ids.
func optionIDs(req *acp.RequestPermissionRequest) string {
	var ids []string
	for _, o := range req.Options {
		ids = append(ids, string(o.OptionID))
	}
	return strings.Join(ids, " ")
}

// ExitPlanMode asks to start in a mode, which claude switches to and
// the client is told of, or to keep planning, which stops the turn.
// Auto mode is offered only on a model that has it.
func TestExitPlanMode(t *testing.T) {
	plan := `{"plan":"Do x"}`
	inPlan := `{"current_permission_mode":"plan"}`
	noAuto := `{"current_permission_mode":"plan","models":[{"value":"default","displayName":"Default"}]}`
	for _, tc := range []struct {
		init, pick, options, answer, mode, stop string
	}{
		{inPlan, optPlanAuto, "exit-plan-auto exit-plan-accept-edits exit-plan-default reject",
			`{"behavior":"allow","updatedInput":` + plan + `,"updatedPermissions":[{"type":"setMode","mode":"auto","destination":"session"}],"decisionClassification":"user_temporary"}`,
			"auto", "end_turn"},
		{noAuto, optPlanEdits, "exit-plan-accept-edits exit-plan-default reject",
			`{"behavior":"allow","updatedPermissions":[{"type":"setMode","mode":"acceptEdits","destination":"session"}]}`, "acceptEdits", "end_turn"},
		{inPlan, optPlanManual, "exit-plan-auto exit-plan-accept-edits exit-plan-default reject",
			`{"behavior":"allow","updatedPermissions":[{"type":"setMode","mode":"default","destination":"session"}]}`, "default", "end_turn"},
		{inPlan, optReject, "exit-plan-auto exit-plan-accept-edits exit-plan-default reject",
			`{"behavior":"deny","message":"The user wants to keep planning.","interrupt":true,"decisionClassification":"user_reject"}`, "", "cancelled"},
		{inPlan, "", "exit-plan-auto exit-plan-accept-edits exit-plan-default reject", `{"behavior":"deny","interrupt":true}`, "", "end_turn"},
		// Auto mode wasn't offered: picking it anyway leaves plan mode for nothing.
		{noAuto, optPlanAuto, "exit-plan-accept-edits exit-plan-default reject", `{"behavior":"deny","interrupt":true}`, "", "end_turn"},
	} {
		t.Run(tc.pick+" "+tc.options, func(t *testing.T) {
			f := started(tc.init, `null`, user("id2"), echo("id2"),
				assistant(`{"type":"tool_use","id":"toolu_1","name":"ExitPlanMode","input":`+plan+`}`),
				canUseTool("toolu_1", "ExitPlanMode", plan, ""), answers("toolu_1", tc.answer), toolDone,
				result(names("id2")), claudetest.Send(idle))
			p := start(t, nil, f)
			var options string
			p.permit = func(ctx context.Context, req *acp.RequestPermissionRequest) (*acp.RequestPermissionResponse, error) {
				options = optionIDs(req)
				return pick(tc.pick)(ctx, req)
			}
			if got := stopReason(p.prompt(t, p.open(t), "go")); got != tc.stop {
				t.Errorf("stop %s", got)
			}
			if options != tc.options {
				t.Errorf("options %q", options)
			}
			var want []string
			if tc.mode != "" {
				want = []string{tc.mode}
			}
			if got := p.modeUpdates(); !slices.Equal(got, want) {
				t.Errorf("modes %q", got)
			}
			if tc.mode != "" && len(p.of("config_option_update")) != 1 {
				t.Errorf("%d option updates", len(p.of("config_option_update")))
			}
		})
	}
}

// EnterPlanMode asks Yes or No; claude in plan mode once it has run is
// news to the client.
func TestEnterPlanMode(t *testing.T) {
	f := fake(user("id2"), echo("id2"),
		assistant(`{"type":"tool_use","id":"toolu_1","name":"EnterPlanMode","input":{}}`),
		canUseTool("toolu_1", "EnterPlanMode", `{}`, ""), answers("toolu_1", `{"behavior":"allow"}`),
		hookCall("toolu_1", "EnterPlanMode", `{}`)[0], hookCall("toolu_1", "EnterPlanMode", `{}`)[1], toolDone,
		result(names("id2")), claudetest.Send(idle))
	p := start(t, nil, f)
	var names []string
	p.permit = func(ctx context.Context, req *acp.RequestPermissionRequest) (*acp.RequestPermissionResponse, error) {
		for _, o := range req.Options {
			names = append(names, string(o.OptionID)+" "+o.Name)
		}
		return pick(optAllowOnce)(ctx, req)
	}
	if got := stopReason(p.prompt(t, p.open(t), "go")); got != "end_turn" {
		t.Fatal(got)
	}
	if want := []string{"allow-once Yes, plan first", "reject No, start now"}; !slices.Equal(names, want) {
		t.Errorf("%q", names)
	}
	if got := p.modeUpdates(); !slices.Equal(got, []string{"plan"}) {
		t.Errorf("modes %q", got)
	}
}

// ExitPlanMode's answer is checked against the options the user was
// shown: Auto mode picked on a model that lost it while the user read
// the plan starts the work accepting edits, rather than cancel it.
func TestExitPlanModeModelChanged(t *testing.T) {
	plan := `{"plan":"Do x"}`
	init := `{"current_permission_mode":"plan","models":[{"value":"default","displayName":"Default","supportsAutoMode":true},` +
		`{"value":"plain","displayName":"Plain"}]}`
	f := started(init, `null`, user("id2"), echo("id2"),
		assistant(`{"type":"tool_use","id":"toolu_1","name":"ExitPlanMode","input":`+plan+`}`),
		canUseTool("toolu_1", "ExitPlanMode", plan, ""),
		answers("toolu_1", `{"behavior":"allow","updatedPermissions":[{"type":"setMode","mode":"acceptEdits","destination":"session"}]}`),
		toolDone, result(names("id2")), claudetest.Send(idle))
	f.Respond(`{"type":"control_request","request":{"subtype":"set_model"}}`,
		`{"type":"control_response","response":{"subtype":"success","request_id":"$req","response":{}}}`)
	p := start(t, nil, f)
	sid := p.open(t)
	var options string
	p.permit = func(ctx context.Context, req *acp.RequestPermissionRequest) (*acp.RequestPermissionResponse, error) {
		options = optionIDs(req)
		if _, err := p.SetSessionConfigOption(ctx, &acp.SetSessionConfigOptionRequest{SessionID: sid, ConfigID: "model",
			ValueID: &acp.SetSessionConfigOptionRequestValueID{Value: "plain"}}); err != nil {
			t.Error(err)
		}
		return pick(optPlanAuto)(ctx, req)
	}
	if got := stopReason(p.prompt(t, sid, "go")); got != "end_turn" {
		t.Errorf("stop %s", got)
	}
	if !strings.HasPrefix(options, optPlanAuto) {
		t.Errorf("options %q", options)
	}
	if got := p.modeUpdates(); !slices.Equal(got, []string{"acceptEdits"}) {
		t.Errorf("modes %q", got)
	}
}
