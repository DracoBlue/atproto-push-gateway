package jetstream

import "testing"

func TestSanitizePostText(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"empty", "", ""},
		{"plain", "hello world", "hello world"},
		{"newline to space", "line1\nline2", "line1 line2"},
		{"crlf to space", "line1\r\nline2", "line1 line2"},
		{"tab to space", "a\tb", "a b"},
		{"collapse spaces", "a   b", "a b"},
		{"collapse mixed whitespace", "a \n\t b", "a b"},
		{"trim leading/trailing", "  hello  ", "hello"},
		{"emoji preserved", "hi 👋 there", "hi 👋 there"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizePostText(tt.in); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTruncatePostText(t *testing.T) {
	tests := []struct {
		name, in string
		max      int
		want     string
	}{
		{"under limit", "hello", 10, "hello"},
		{"at limit", "hello", 5, "hello"},
		{"over limit", "hello world", 5, "hello…"},
		{"zero means no limit", "hello world", 0, "hello world"},
		{"negative means no limit", "hello world", -1, "hello world"},
		{"multibyte runes", "äöüß!!", 4, "äöüß…"},
		{"emoji counted as one rune", "👋👋👋👋👋", 3, "👋👋👋…"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := truncatePostText(tt.in, tt.max); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSetPostTextMaxGraphemes(t *testing.T) {
	c := &Consumer{postTextMaxGraphemes: defaultPostTextMaxGraphemes}
	c.SetPostTextMaxGraphemes(50)
	if c.postTextMaxGraphemes != 50 {
		t.Errorf("expected 50, got %d", c.postTextMaxGraphemes)
	}
	c.SetPostTextMaxGraphemes(0) // ignored
	if c.postTextMaxGraphemes != 50 {
		t.Errorf("expected 0 to be ignored, got %d", c.postTextMaxGraphemes)
	}
	c.SetPostTextMaxGraphemes(-1) // ignored
	if c.postTextMaxGraphemes != 50 {
		t.Errorf("expected negative to be ignored, got %d", c.postTextMaxGraphemes)
	}
}
