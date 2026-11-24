package sessions

import (
	"strings"
	"unicode/utf8"
)

// MaxListTitle is the most characters ListTitle leaves in a title.
const MaxListTitle = 256

// ListTitle is a session's title as a list of sessions shows it
// (claudeacp's session/list does): s (an Info's Summary, say) on one line,
// every run of white space one space, with none at either end, and cut to
// MaxListTitle characters, the last of them "…" when it was cut.
func ListTitle(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= MaxListTitle {
		return s
	}
	return string([]rune(s)[:MaxListTitle-1]) + "…"
}
