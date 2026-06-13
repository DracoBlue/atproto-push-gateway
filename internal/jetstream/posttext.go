package jetstream

import (
	"strings"
	"unicode/utf8"
)

// defaultPostTextMaxGraphemes mirrors the Bluesky post-length limit
// (`MAX_GRAPHEMES = 300` in social-app). Used when
// PUSH_POST_TEXT_MAX_GRAPHEMES is unset.
const defaultPostTextMaxGraphemes = 300

// sanitizePostText collapses newlines and tabs to single spaces and trims
// surrounding whitespace, producing a lockscreen-friendly single line.
func sanitizePostText(s string) string {
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	prevSpace := false
	for _, r := range s {
		if r == '\n' || r == '\r' || r == '\t' {
			r = ' '
		}
		if r == ' ' {
			if prevSpace {
				continue
			}
			prevSpace = true
		} else {
			prevSpace = false
		}
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

// truncatePostText cuts s to at most max runes, appending an ellipsis when
// truncation occurred. We use rune (codepoint) count as a stand-in for
// grapheme count; this slightly under-counts text with combining characters
// but stays within payload limits and avoids an extra dependency.
// max <= 0 means no limit.
func truncatePostText(s string, max int) string {
	if max <= 0 {
		return s
	}
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	count := 0
	for i := range s {
		if count == max {
			return s[:i] + "…"
		}
		count++
	}
	return s
}
