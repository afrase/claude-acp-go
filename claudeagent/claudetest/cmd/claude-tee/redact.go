package main

import (
	"bytes"
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
)

// secretKeys are the keys whose values a cassette never keeps, only their
// names: an MCP server's environment and HTTP headers (in --mcp-config,
// --settings, initialize and mcp_set_servers), and the settings' env,
// which may hold an API key.
var secretKeys = map[string]bool{"env": true, "headers": true}

// redacted stands in for a value a cassette doesn't keep; inURL in a URL,
// where it stays a valid one.
const (
	redacted = "<redacted>"
	inURL    = "redacted"
)

// secretWords are what a name holds when it likely names a credential:
// a flag's, a query parameter's, a variable's or a header's.
var secretWords = []string{"token", "key", "secret", "pass", "auth", "credential", "signature", "cookie", "session"}

// secretName says whether name looks like it names a credential. It errs
// towards yes: a value kept out of a cassette costs little.
func secretName(name string) bool {
	n := strings.ToLower(name)
	for _, w := range secretWords {
		if strings.Contains(n, w) {
			return true
		}
	}
	return false
}

var (
	envName    = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)
	identifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]*$`)
	headerName = regexp.MustCompile(`^[A-Za-z0-9-]+$`)
)

// redactURL is s with the credentials of a URL taken out: its userinfo
// and the values of query parameters with secret names. Its path is
// kept, though some servers put a token there. s is unchanged, with
// false, when it isn't an absolute URL or has none.
func redactURL(s string) (string, bool) {
	u, err := url.Parse(s)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return s, false
	}
	changed := false
	if u.User != nil && u.User.String() != inURL {
		u.User = url.User(inURL)
		changed = true
	}
	if u.RawQuery != "" {
		// Parameter by parameter, so their order and spelling stay.
		params := strings.Split(u.RawQuery, "&")
		for i, p := range params {
			k, v, _ := strings.Cut(p, "=")
			name, err := url.QueryUnescape(k)
			if err == nil && secretName(name) && v != inURL {
				params[i] = k + "=" + inURL
				changed = true
			}
		}
		u.RawQuery = strings.Join(params, "&")
	}
	if !changed {
		return s, false
	}
	return u.String(), true
}

// redactValue is an argument with the credential it carries taken out:
// NAME=value for an environment variable or a secret name, "Name: value"
// for a header with a secret name (as --header takes), or a URL's.
func redactValue(s string) (string, bool) {
	if name, value, ok := strings.Cut(s, "="); ok && identifier.MatchString(name) && (envName.MatchString(name) || secretName(name)) {
		return name + "=" + redacted, value != redacted
	}
	if name, value, ok := strings.Cut(s, ":"); ok && headerName.MatchString(name) && secretName(name) {
		return name + ": " + redacted, strings.TrimSpace(value) != redacted
	}
	return redactURL(s)
}

// redactArgList takes credentials out of an MCP server's args, in place:
// the value of a flag with a secret name (--token=x, or --api-key x),
// and what redactValue takes out of the rest. It says whether it changed
// any.
func redactArgList(args []any) bool {
	changed := false
	for i := 0; i < len(args); i++ {
		s, ok := args[i].(string)
		if !ok {
			continue
		}
		flag, value, hasValue := strings.Cut(s, "=")
		switch {
		case !strings.HasPrefix(s, "-"):
			if r, ok := redactValue(s); ok {
				args[i], changed = r, true
			}
		case !secretName(strings.TrimLeft(flag, "-")):
			if r, ok := redactValue(value); hasValue && ok {
				args[i], changed = flag+"="+r, true
			}
		case hasValue:
			if value != redacted {
				args[i], changed = flag+"="+redacted, true
			}
		case i+1 < len(args):
			if next, ok := args[i+1].(string); ok && !strings.HasPrefix(next, "-") {
				if next != redacted {
					args[i+1], changed = redacted, true
				}
				i++
			}
		}
	}
	return changed
}

// redactArgs is args with the secrets in any JSON value taken out: a JSON
// argument, or the value of --flag=JSON.
func redactArgs(args []string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = a
		flag, value, isFlag := strings.Cut(a, "=")
		if !isFlag || !strings.HasPrefix(flag, "-") {
			flag, value = "", a
		}
		if r, ok := redactJSON([]byte(value)); ok {
			out[i] = string(r)
			if flag != "" {
				out[i] = flag + "=" + string(r)
			}
		}
	}
	return out
}

// redactJSON is raw with every value under a secretKeys object replaced,
// and true, when raw is JSON holding one; else raw and false, untouched,
// so a frame without secrets keeps its bytes and its keys' order. Every
// object or array is decoded to look: a key can be spelled with escapes.
func redactJSON(raw []byte) ([]byte, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' && raw[0] != '[' {
		return raw, false
	}
	var v any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if d.Decode(&v) != nil || !redact(v) {
		return raw, false
	}
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	if err := e.Encode(v); err != nil {
		return raw, false
	}
	return bytes.TrimSpace(b.Bytes()), true
}

// redact replaces, in place, the values of each secretKeys object in v
// and the credentials in a "url" string or an "args" list (an MCP
// server's), and says whether it replaced any.
func redact(v any) bool {
	changed := false
	switch v := v.(type) {
	case map[string]any:
		for k, x := range v {
			changed = redactField(v, k, x) || changed
		}
	case []any:
		for _, x := range v {
			changed = redact(x) || changed
		}
	}
	return changed
}

// redactField redacts obj's field k, whose value is x.
func redactField(obj map[string]any, k string, x any) bool {
	m, isObject := x.(map[string]any)
	s, isString := x.(string)
	list, isList := x.([]any)
	switch {
	case isObject && secretKeys[k]:
		changed := false
		for name, value := range m {
			if value != redacted {
				m[name], changed = redacted, true
			}
		}
		return changed
	case isString && k == "url":
		r, ok := redactURL(s)
		if ok {
			obj[k] = r
		}
		return ok
	case isList && k == "args":
		changed := redactArgList(list)
		return redact(list) || changed // what isn't a string is looked into as anywhere
	}
	return redact(x)
}
