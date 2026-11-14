package claudeacp

import (
	"bytes"
	"encoding/json"
	"io"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// claude's settings, as far as the session's options show them: claude
// merges its settings files (the user's, the project's, the local and the
// managed ones) under those the session gave, a JSON object or a file's
// path (from cwd). An option the settings set shows what claude runs
// with, and an effort change carries ultracode on when they turned it on,
// as claude-agent-acp reads them. They are read as data only.

// claudeSettings are the keys of claude's settings the options follow.
type claudeSettings struct {
	Ultracode     bool                     `json:"ultracode"`
	EffortLevel   string                   `json:"effortLevel"`
	FastMode      bool                     `json:"fastMode"`
	Agent         string                   `json:"agent"`
	ModelSettings map[string]modelSettings `json:"modelSettings"`
}

// modelSettings are a model's own settings.
type modelSettings struct {
	EffortLevel string `json:"effortLevel"`
}

// maxSettings is the most of a settings file read.
const maxSettings = 1 << 20

// readSettings are claude's settings for a session in cwd: the files
// under configDir and cwd, then given (o.Settings) over them.
func readSettings(configDir, cwd, given string) claudeSettings {
	layers := []string{
		filepath.Join(configDir, "settings.json"),
		filepath.Join(cwd, ".claude", "settings.json"),
		filepath.Join(cwd, ".claude", "settings.local.json"),
		managedSettings,
	}
	merged := map[string]json.RawMessage{}
	for _, path := range layers {
		if path != "" {
			mergeSettings(merged, readSettingsFile(path))
		}
	}
	switch {
	case strings.HasPrefix(strings.TrimSpace(given), "{"):
		mergeSettings(merged, []byte(given))
	case given != "":
		if !filepath.IsAbs(given) {
			given = filepath.Join(cwd, given)
		}
		mergeSettings(merged, readSettingsFile(given))
	}
	var s claudeSettings
	b, err := json.Marshal(merged)
	if err == nil {
		_ = json.Unmarshal(b, &s) // a key of another type tells nothing
	}
	return s
}

// mergeSettings puts layer's keys over into's; a model's own settings
// replace that model's alone.
func mergeSettings(into map[string]json.RawMessage, layer []byte) {
	var m map[string]json.RawMessage
	if json.Unmarshal(layer, &m) != nil {
		return
	}
	for k, v := range m {
		if k != "modelSettings" {
			into[k] = v
			continue
		}
		var base, over map[string]json.RawMessage
		_ = json.Unmarshal(into[k], &base)
		if json.Unmarshal(v, &over) != nil {
			continue
		}
		for key := range base {
			for o := range over {
				if modelKey(key) == modelKey(o) {
					delete(base, key)
				}
			}
		}
		if base == nil {
			base = map[string]json.RawMessage{}
		}
		maps.Copy(base, over)
		if b, err := json.Marshal(base); err == nil {
			into[k] = b
		}
	}
}

// readSettingsFile is a settings file's bytes; nil when it isn't a
// regular file that can be read.
func readSettingsFile(path string) []byte {
	f, err := os.Open(path) //nolint:gosec // claude's settings files, read as data
	if err != nil {
		return nil
	}
	defer f.Close() //nolint:errcheck // read-only
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		return nil
	}
	b, err := io.ReadAll(io.LimitReader(f, maxSettings))
	if err != nil {
		return nil
	}
	return bytes.TrimSpace(b)
}

// managedSettings is where an administrator's settings for claude are
// ("": none, for tests).
var managedSettings = func() string {
	switch runtime.GOOS {
	case "darwin":
		return "/Library/Application Support/ClaudeCode/managed-settings.json"
	case "windows":
		return `C:\Program Files\ClaudeCode\managed-settings.json`
	}
	return "/etc/claude-code/managed-settings.json"
}()

// effortFor is the effort the settings give the model (its value and id,
// resolved): its own setting first, then the general one.
func (s claudeSettings) effortFor(keys ...string) string {
	for _, k := range keys {
		if k == "" {
			continue
		}
		for name, m := range s.ModelSettings {
			if m.EffortLevel != "" && modelKey(name) == modelKey(k) {
				return m.EffortLevel
			}
		}
	}
	return s.EffortLevel
}

// modelKey is a model's name as its settings are keyed: lower case, a
// "-1m" ending written "[1m]".
func modelKey(name string) string {
	k := strings.ToLower(strings.TrimSpace(name))
	if i := strings.LastIndex(k, "-"); i >= 0 && strings.HasSuffix(k, "m") && isDigits(k[i+1:len(k)-1]) {
		k = k[:i] + "[" + k[i+1:] + "]"
	}
	return k
}

func isDigits(s string) bool {
	return s != "" && strings.Trim(s, "0123456789") == ""
}

// claudeConfigDir is the config folder claude runs with in env (nil:
// this process's): $CLAUDE_CONFIG_DIR, else ~/.claude.
func claudeConfigDir(env []string) string {
	if env == nil {
		env = os.Environ()
	}
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, configDirVar+"="); ok && v != "" {
			return v
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude")
}
