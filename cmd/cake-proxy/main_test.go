package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

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

func TestHostGuard(t *testing.T) {
	handler := hostGuard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}), loopbackHosts("127.0.0.1:8787"))

	cases := map[string]int{
		"127.0.0.1:8787": http.StatusNoContent,
		"localhost:8787": http.StatusNoContent,
		"[::1]:8787":     http.StatusNoContent,
		"127.0.0.1":      http.StatusNoContent,
		"evil.example":   http.StatusMisdirectedRequest,
	}
	for host, want := range cases {
		request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8787/", nil)
		request.Host = host
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != want {
			t.Errorf("Host %q = %d, want %d", host, response.Code, want)
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
