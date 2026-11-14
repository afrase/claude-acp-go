package fakeapi

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A real-looking Anthropic key is refused in either header claude sends
// one in, on every path, and is neither kept nor logged; a dummy one is
// served.
func TestRefusesRealKey(t *testing.T) {
	const apiKey, oauth = "sk-ant-api03-SECRETKEY", "sk-ant-oat01-SECRETTOKEN"
	var logged bytes.Buffer
	f := New(&Script{Turns: []Turn{{Content: []Block{{Type: "text", Text: "hi"}}}}}, slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelDebug})))
	srv := httptest.NewServer(f)
	defer srv.Close()
	headers := []struct{ name, value string }{
		{"X-Api-Key", apiKey},
		{"x-api-key", " " + apiKey},
		{"Authorization", "Bearer " + oauth},
		{"Authorization", "Bearer " + apiKey},
	}
	calls := []struct{ method, path, body string }{
		{"POST", "/v1/messages?beta=true", withTools},
		{"POST", "/v1/messages/count_tokens?beta=true", withTools},
		{"GET", "/v1/models", ""},
	}
	do := func(method, path, body, header, value string) int {
		t.Helper()
		req, err := http.NewRequestWithContext(context.Background(), method, srv.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set(header, value)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	for _, h := range headers {
		for _, c := range calls {
			if got := do(c.method, c.path, c.body, h.name, h.value); got != http.StatusUnauthorized {
				t.Errorf("%s %s with %s: %d, want 401", c.method, c.path, h.name, got)
			}
		}
	}
	if f.Left() != 1 {
		t.Errorf("a refused request took a turn: %d left", f.Left())
	}
	for _, h := range []string{"X-Api-Key", "Authorization"} {
		if got := do("POST", "/v1/messages", withTools, h, "dummy"); got != http.StatusOK {
			t.Errorf("a dummy key in %s: %d", h, got)
		}
	}
	for _, s := range f.Served() {
		if strings.Contains(s.Method+s.Path+s.Model+s.Answer, "SECRET") {
			t.Errorf("a key was kept: %+v", s)
		}
	}
	if strings.Contains(logged.String(), "SECRET") || !strings.Contains(logged.String(), "refused") {
		t.Errorf("the log:\n%s", logged.String())
	}
}
