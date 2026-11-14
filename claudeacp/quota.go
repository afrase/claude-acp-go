package claudeacp

import (
	"maps"
	"slices"

	"github.com/afrase/claude-acp-go/claudeagent"
)

// A prompt's answer says what its turn spent under _meta.quota, as
// claude-agent-acp's does (in codex-acp's shape): token_count is the
// answer's usage, the main loop's; model_usage splits by model what the
// turn's results added to claude's running modelUsage, subagents and
// compaction included, so the rows can add up to more.

// spend is tokens by model.
type spend map[string]claudeagent.Usage

// spendOf is a result's modelUsage, the session's running total.
func spendOf(mu map[string]claudeagent.ModelUsage) spend {
	out := spend{}
	for m, u := range mu {
		out[m] = claudeagent.Usage{InputTokens: u.InputTokens, OutputTokens: u.OutputTokens,
			CacheReadInputTokens: u.CacheReadInputTokens, CacheCreationInputTokens: u.CacheCreationInputTokens}
	}
	return out
}

// since is what sp added to prev, the models that spent nothing left
// out. A model whose count fell (claude's total restarted: a /clear) has
// spent all of sp's.
func (sp spend) since(prev spend) spend {
	out := spend{}
	for m, u := range sp {
		d := u
		if b, ok := prev[m]; ok {
			d = claudeagent.Usage{InputTokens: u.InputTokens - b.InputTokens, OutputTokens: u.OutputTokens - b.OutputTokens,
				CacheReadInputTokens:     u.CacheReadInputTokens - b.CacheReadInputTokens,
				CacheCreationInputTokens: u.CacheCreationInputTokens - b.CacheCreationInputTokens}
			if d.InputTokens < 0 || d.OutputTokens < 0 || d.CacheReadInputTokens < 0 || d.CacheCreationInputTokens < 0 {
				d = u
			}
		}
		if total(d) > 0 {
			out[m] = d
		}
	}
	return out
}

// add is sp and b together.
func (sp spend) add(b spend) spend {
	out := maps.Clone(sp)
	if out == nil {
		out = spend{}
	}
	for m, u := range b {
		out[m] = addUsage(out[m], u)
	}
	return out
}

// resultSpend is what result r spent by model, and the reading the next
// result's is measured from. The session's first result has no reading
// before it, and a resumed session's counts its history too: its turn's
// own usage stands in, under model, the last top-level message's.
func (st *stream) resultSpend(r *claudeagent.Result, model string) spend {
	reading := spendOf(r.ModelUsage)
	defer func() { st.lastSpend = reading }()
	if st.lastSpend == nil {
		if model == "" || total(r.Usage) == 0 {
			return nil
		}
		return spend{model: r.Usage}
	}
	return reading.since(st.lastSpend)
}

func total(u claudeagent.Usage) int {
	return u.InputTokens + u.OutputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens
}

// quotaMeta is the _meta of a prompt's answer: the turn's usage u and
// its spend by model.
func quotaMeta(u claudeagent.Usage, models spend) map[string]any {
	rows := []map[string]any{}
	for _, m := range slices.Sorted(maps.Keys(models)) {
		rows = append(rows, map[string]any{"model": m, "token_count": tokenCount(models[m])})
	}
	return map[string]any{"quota": map[string]any{"token_count": tokenCount(u), "model_usage": rows}}
}

// tokenCount is u as the quota says it; claude counts thinking in its
// output tokens, so reasoningOutputTokens is 0.
func tokenCount(u claudeagent.Usage) map[string]any {
	return map[string]any{"totalTokens": total(u), "inputTokens": u.InputTokens, "cachedInputTokens": u.CacheReadInputTokens,
		"cachedWriteTokens": u.CacheCreationInputTokens, "outputTokens": u.OutputTokens, "reasoningOutputTokens": 0}
}
