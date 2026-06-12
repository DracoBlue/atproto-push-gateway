package push

import "testing"

func TestTokenForLog_RedactsByDefault(t *testing.T) {
	SetDebugLogging(false)
	if got := tokenForLog("ExponentPushToken[secret-token-value]"); got != "[redacted]" {
		t.Errorf("expected [redacted] without debug logging, got %q", got)
	}
}

func TestTokenForLog_TruncatesInDebug(t *testing.T) {
	SetDebugLogging(true)
	defer SetDebugLogging(false)
	if got := tokenForLog("ExponentPushToken[secret-token-value]"); got != "ExponentPushToken[se..." {
		t.Errorf("expected truncated token in debug mode, got %q", got)
	}
	if got := tokenForLog("short"); got != "short" {
		t.Errorf("expected short token unchanged in debug mode, got %q", got)
	}
}
