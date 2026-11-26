package tunnel

import "testing"

func TestTunnelURLFollowsTheServerScheme(t *testing.T) {
	cases := map[string]string{
		"https://api.example.com":   "wss://api.example.com/connect/v1/tunnel",
		"https://api.example.com/":  "wss://api.example.com/connect/v1/tunnel",
		"http://127.0.0.1:8080":     "ws://127.0.0.1:8080/connect/v1/tunnel",
		"https://example.com/base/": "wss://example.com/base/connect/v1/tunnel",
	}
	for in, want := range cases {
		if got := TunnelURL(in); got != want {
			t.Errorf("TunnelURL(%q) = %q, want %q", in, got, want)
		}
	}
}
