package claudetest

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Cassette is a recorded run of claude: how it was started, and every
// line on its stdin and stdout, in order. On disk it is JSON Lines: the
// Header, then a Frame a line.
type Cassette struct {
	Header Header
	Frames []Frame
}

// Header is how the recorded claude was started. It has the environment's
// names only, never their values, which may be secrets.
type Header struct {
	Argv    []string `json:"argv"`    // the arguments after claude's path
	EnvKeys []string `json:"envKeys"` // the environment's names
	CLI     string   `json:"cli"`     // claude's version
}

// Frame is one line of the run: "in" written to claude's stdin, "out"
// written by claude on stdout. MS is when, in milliseconds from the start;
// Replay plays in order and ignores it.
type Frame struct {
	Dir   string          `json:"dir"`
	MS    int64           `json:"ms"`
	Frame json.RawMessage `json:"frame"`
}

// ReadCassette reads a cassette from r.
func ReadCassette(r io.Reader) (*Cassette, error) {
	br := bufio.NewReader(r)
	var c Cassette
	for n := 1; ; n++ {
		line, err := br.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			if perr := c.parse(n, line); perr != nil {
				return nil, perr
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	if c.Header.Argv == nil {
		return nil, errors.New("claudetest: the cassette has no header")
	}
	return &c, nil
}

// parse takes line n: the header first, then frames.
func (c *Cassette) parse(n int, line []byte) error {
	if c.Header.Argv == nil {
		if err := json.Unmarshal(line, &c.Header); err != nil || c.Header.Argv == nil {
			return fmt.Errorf("claudetest: cassette line %d isn't a header", n)
		}
		return nil
	}
	var f Frame
	if err := json.Unmarshal(line, &f); err != nil || f.Dir != "in" && f.Dir != "out" || len(f.Frame) == 0 {
		return fmt.Errorf("claudetest: cassette line %d isn't a frame", n)
	}
	c.Frames = append(c.Frames, f)
	return nil
}

// Replay is a fake claude that plays c: it expects each line written to
// the recorded claude, matched on what the client decided (its kind, a
// request's arguments, an answer's outcome and the request it answers, a
// message's content: expectRecorded has the rule), and writes each line
// the recorded claude wrote, in the recorded order. The client's ids stand in for the recorded ones in what
// it writes: the session's (from --session-id and --resume), requests'
// and user messages', so responses and echoes carry the live ones.
func Replay(c *Cassette) *Fake {
	ids := map[string]string{}
	steps := []Step{{name: "the session's id", run: func(r *run) error {
		live := flags(r.f.Spec().Args)
		for k, rec := range flags(c.Header.Argv) {
			if l := live[k]; l != "" {
				ids[rec] = l
			}
		}
		return nil
	}}}
	for _, f := range c.Frames {
		var head struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(f.Frame, &head)
		switch {
		case f.Dir == "in" && head.Type == "keep_alive": // the fake never sees these
		case f.Dir == "in":
			steps = append(steps, expectRecorded(f.Frame, ids))
		default:
			steps = append(steps, sendRecorded(f.Frame, ids))
		}
	}
	return New(steps...)
}

// flags are the values of the flags that name the session.
func flags(argv []string) map[string]string {
	out := map[string]string{}
	for i, a := range argv {
		for _, k := range []string{"--session-id", "--resume"} {
			switch {
			case strings.HasPrefix(a, k+"="):
				out[k] = strings.TrimPrefix(a, k+"=")
			case a == k && i+1 < len(argv):
				out[k] = argv[i+1]
			}
		}
	}
	return out
}

// expectRecorded expects a line like rec, and maps rec's request id and
// uuid to the line's.
//
// It checks what the client decided, not the names it gave or the time:
// the line's type, subtype and request's subtype; a control_request's
// other plain fields (stop_task's task_id, set_model's model), except an
// initialize's, which is the client's own setup; a control_response's
// subtype, the request it answers and a permission's behavior (allow or
// deny); a user message's content. Ids, uuids and session ids are read
// as the live ones the replay has mapped them to; other fields, such as
// timestamps, hooks and options, aren't checked.
func expectRecorded(rec json.RawMessage, ids map[string]string) Step {
	var r struct {
		Type      string `json:"type"`
		Subtype   string `json:"subtype"`
		RequestID string `json:"request_id"`
		UUID      string `json:"uuid"`
		Request   struct {
			Subtype string `json:"subtype"`
		} `json:"request"`
	}
	_ = json.Unmarshal(rec, &r)
	var recorded any
	_ = json.Unmarshal(rec, &recorded)
	fields, _ := recorded.(map[string]any)
	return Step{name: strings.TrimSpace("expect recorded " + r.Type + " " + r.Subtype + r.Request.Subtype), wants: recorded, run: func(run *run) error {
		// Built now, not before: the ids the line names are mapped as
		// the replay plays.
		req, uuid, err := run.expect(decided(fields, ids))
		if err != nil {
			return err
		}
		for rec, live := range map[string]string{r.RequestID: req, r.UUID: uuid} {
			if rec != "" && live != "" {
				ids[rec] = live
			}
		}
		return nil
	}}
}

// decided is the part of a recorded stdin line a replay checks (see
// expectRecorded), as a match for matches, with ids mapped to the live
// ones.
func decided(rec map[string]any, ids map[string]string) map[string]any {
	live := func(v any) any {
		if s, ok := v.(string); ok && ids[s] != "" {
			return ids[s]
		}
		return v
	}
	want := map[string]any{"type": rec["type"]}
	if v, ok := rec["subtype"]; ok {
		want["subtype"] = v
	}
	switch rec["type"] {
	case "control_request":
		request, _ := rec["request"].(map[string]any)
		w := map[string]any{}
		for k, v := range request {
			switch v.(type) {
			case string, float64, bool:
				if k == "subtype" || request["subtype"] != "initialize" {
					w[k] = live(v)
				}
			}
		}
		if len(w) > 0 {
			want["request"] = w
		}
	case "control_response":
		response, _ := rec["response"].(map[string]any)
		w := map[string]any{}
		for _, k := range []string{"subtype", "request_id"} {
			if v, ok := response[k]; ok {
				w[k] = live(v)
			}
		}
		if answer, _ := response["response"].(map[string]any); answer["behavior"] != nil {
			w["response"] = map[string]any{"behavior": answer["behavior"]}
		}
		want["response"] = w
	case "user":
		if message, _ := rec["message"].(map[string]any); message != nil {
			if content, ok := message["content"]; ok {
				want["message"] = map[string]any{"content": content}
			}
		}
	}
	return want
}

// sendRecorded writes rec with the live ids for the recorded ones.
func sendRecorded(rec json.RawMessage, ids map[string]string) Step {
	return Step{name: "send recorded", run: func(r *run) error {
		pairs := make([]string, 0, 2*len(ids))
		for rec, live := range ids {
			pairs = append(pairs, `"`+rec+`"`, `"`+live+`"`) // whole JSON strings, never part of one
		}
		_, err := io.WriteString(r.f.stdout, strings.NewReplacer(pairs...).Replace(string(rec))+"\n")
		return err
	}}
}
