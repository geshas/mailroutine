package httpapi

import (
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/geshas/mailroutine/internal/config"
)

func TestRecovererTurnsPanicsInto500(t *testing.T) {
	s := &Server{log: slog.New(slog.DiscardHandler)}
	h := s.recoverer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") }))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil))
	if rec.Code != 500 || rec.Body.Len() == 0 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestClientIPResolution(t *testing.T) {
	_, proxies, _ := net.ParseCIDR("10.0.0.0/8")
	s := &Server{Deps: Deps{Config: &config.Config{TrustedProxies: []*net.IPNet{proxies}}}}
	req := func(remote, xff string) *http.Request {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = remote
		if xff != "" {
			r.Header.Set("X-Forwarded-For", xff)
		}
		return r
	}
	cases := []struct{ name, remote, xff, want string }{
		{"direct client", "203.0.113.5:1234", "", "203.0.113.5"},
		{"remote without port", "203.0.113.5", "", "203.0.113.5"},
		{"untrusted peer cannot spoof", "203.0.113.5:1", "1.2.3.4", "203.0.113.5"},
		{"trusted proxy forwards client", "10.0.0.2:1", "198.51.100.9", "198.51.100.9"},
		{"rightmost untrusted hop wins", "10.0.0.2:1", "6.6.6.6, 198.51.100.9, 10.0.0.7", "198.51.100.9"},
		{"garbage header falls back to peer", "10.0.0.2:1", "not-an-ip", "10.0.0.2"},
		{"all hops trusted", "10.0.0.2:1", "10.0.0.8, 10.0.0.9", "10.0.0.2"},
		{"unparseable peer", "weird", "1.2.3.4", "weird"},
	}
	for _, c := range cases {
		if got := s.clientIP(req(c.remote, c.xff)); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

func TestSanitizeCodeTruncates(t *testing.T) {
	long := ""
	for i := 0; i < 100; i++ {
		long += "a"
	}
	if got := sanitizeCode(long); len(got) != 40 {
		t.Fatal(len(got))
	}
}
