package main

import "testing"

func TestPublicBase(t *testing.T) {
	cases := map[string]string{
		"127.0.0.1:8787": "http://127.0.0.1:8787",
		":8787":          "http://127.0.0.1:8787",
		"0.0.0.0:9000":   "http://127.0.0.1:9000",
		"[::1]:8787":     "http://[::1]:8787",
	}
	for listen, want := range cases {
		if got := publicBase(listen); got != want {
			t.Errorf("publicBase(%q) = %q, want %q", listen, got, want)
		}
	}
}

func TestProviderForUpstream(t *testing.T) {
	cases := map[string]string{
		"https://opencode.ai/inference/go/openai/v1": "opencode",
		"https://openrouter.ai/api/v1":               "openrouter",
		"https://api.example.test/v1":                "",
	}
	for upstream, want := range cases {
		if got := providerForUpstream(upstream); got != want {
			t.Errorf("providerForUpstream(%q) = %q, want %q", upstream, got, want)
		}
	}
}

func TestCheckListen(t *testing.T) {
	cases := []struct {
		listen      string
		allowRemote bool
		ok          bool
	}{
		{"127.0.0.1:8787", false, true},
		{"localhost:8787", false, true},
		{"[::1]:8787", false, true},
		{":8787", false, false},
		{"0.0.0.0:8787", false, false},
		{"0.0.0.0:8787", true, true},
		{"nonsense", false, false},
	}
	for _, tc := range cases {
		err := checkListen(tc.listen, tc.allowRemote)
		if (err == nil) != tc.ok {
			t.Errorf("checkListen(%q, %v) = %v, want ok=%v", tc.listen, tc.allowRemote, err, tc.ok)
		}
	}
}
