package xrpc

import "testing"

func TestURIRKey(t *testing.T) {
	tests := []struct {
		uri  string
		want string
	}{
		{"at://did:plc:alice/app.bsky.graph.block/3kco5r7x", "3kco5r7x"},
		{"at://did:plc:alice/app.bsky.graph.block/", ""},
		{"no-slashes", ""},
		{"", ""},
	}
	for _, tt := range tests {
		if got := uriRKey(tt.uri); got != tt.want {
			t.Errorf("uriRKey(%q) = %q, want %q", tt.uri, got, tt.want)
		}
	}
}
