package localui

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"
	"time"
)

func start(t *testing.T) (*Server, *http.Client) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "page") })
	mux.HandleFunc("/api/do", func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
			return
		}
		_, _ = io.WriteString(w, "did "+string(b))
	})
	s, err := New(mux)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = s.Serve(ctx) }()
	t.Cleanup(func() { cancel(); _ = s.Close() })
	jar, _ := cookiejar.New(nil)
	return s, &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func get(t *testing.T, c *http.Client, u string, mod func(*http.Request)) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, u, nil)
	if mod != nil {
		mod(req)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return resp, string(b)
}

func signIn(t *testing.T, s *Server, c *http.Client) {
	t.Helper()
	u, err := s.NewURL()
	if err != nil {
		t.Fatal(err)
	}
	resp, _ := get(t, c, u, nil)
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("token exchange: %d", resp.StatusCode)
	}
}

func TestListensOnLoopbackOnly(t *testing.T) {
	s, _ := start(t)
	host, _, err := net.SplitHostPort(s.Addr())
	if err != nil || host != "127.0.0.1" {
		t.Fatalf("listens on %q, want 127.0.0.1", s.Addr())
	}
}

func TestNothingIsServedWithoutASession(t *testing.T) {
	s, c := start(t)
	for _, p := range []string{"/", "/api/do", "/?t=wrong", "/?t="} {
		resp, body := get(t, c, "http://"+s.Addr()+p, nil)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("GET %s without a session: %d %q", p, resp.StatusCode, body)
		}
		if strings.Contains(body, "page") {
			t.Errorf("GET %s leaked the page", p)
		}
	}
}

func TestTheTokenWorksOnceAndSetsAStrictCookie(t *testing.T) {
	s, c := start(t)
	u, _ := s.NewURL()
	resp, _ := get(t, c, u, nil)
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/" {
		t.Fatalf("exchange: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	var ck *http.Cookie
	for _, x := range resp.Cookies() {
		if x.Name == cookieName {
			ck = x
		}
	}
	if ck == nil || !ck.HttpOnly || ck.SameSite != http.SameSiteStrictMode || len(ck.Value) < 32 {
		t.Fatalf("the session cookie must be HttpOnly, SameSite=Strict and long: %+v", ck)
	}
	if resp, body := get(t, c, "http://"+s.Addr()+"/", nil); resp.StatusCode != http.StatusOK || body != "page" {
		t.Fatalf("after signing in: %d %q", resp.StatusCode, body)
	}
	// The same address again (from a history entry, a log) is useless.
	other := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if resp, _ := get(t, other, u, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a used token was accepted again: %d", resp.StatusCode)
	}
}

func TestAForgedCookieIsRefused(t *testing.T) {
	s, _ := start(t)
	plain := &http.Client{}
	resp, _ := get(t, plain, "http://"+s.Addr()+"/", func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: cookieName, Value: strings.Repeat("a", 64)})
	})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("forged cookie: %d", resp.StatusCode)
	}
}

func TestAnotherHostNameIsRefusedEvenWithASession(t *testing.T) {
	s, c := start(t)
	signIn(t, s, c)
	for _, host := range []string{"evil.example", "evil.example:" + portOf(s), "localhost:" + portOf(s), "127.0.0.1:1", "[::1]:" + portOf(s)} {
		resp, _ := get(t, c, "http://"+s.Addr()+"/", func(r *http.Request) { r.Host = host })
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("Host %q was served (%d): DNS rebinding would work", host, resp.StatusCode)
		}
	}
}

func portOf(s *Server) string { _, p, _ := net.SplitHostPort(s.Addr()); return p }

func TestAChangingRequestMustComeFromThePage(t *testing.T) {
	s, c := start(t)
	signIn(t, s, c)
	post := func(origin string, set bool, body string) (*http.Response, string) {
		req, _ := http.NewRequest(http.MethodPost, "http://"+s.Addr()+"/api/do", strings.NewReader(body))
		if set {
			req.Header.Set("Origin", origin)
		}
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return resp, string(b)
	}
	if resp, _ := post("", false, "x"); resp.StatusCode != http.StatusForbidden {
		t.Errorf("a POST without Origin: %d", resp.StatusCode)
	}
	if resp, _ := post("http://evil.example", true, "x"); resp.StatusCode != http.StatusForbidden {
		t.Errorf("a POST from another origin: %d", resp.StatusCode)
	}
	if resp, _ := post("http://localhost:"+portOf(s), true, "x"); resp.StatusCode != http.StatusForbidden {
		t.Errorf("a POST from another name of the same machine: %d", resp.StatusCode)
	}
	if resp, body := post("http://"+s.Addr(), true, "x"); resp.StatusCode != http.StatusOK || body != "did x" {
		t.Errorf("a POST from the page: %d %q", resp.StatusCode, body)
	}
}

func TestABigBodyIsRefused(t *testing.T) {
	s, c := start(t)
	signIn(t, s, c)
	req, _ := http.NewRequest(http.MethodPost, "http://"+s.Addr()+"/api/do", strings.NewReader(strings.Repeat("x", 2<<20)))
	req.Header.Set("Origin", "http://"+s.Addr())
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("a 2 MiB body: %d, want 413", resp.StatusCode)
	}
}

func TestRepliesCarryTheSecurityHeadersAndNoCORS(t *testing.T) {
	s, c := start(t)
	signIn(t, s, c)
	resp, _ := get(t, c, "http://"+s.Addr()+"/", nil)
	for h, want := range map[string]string{
		"Cache-Control": "no-store", "X-Content-Type-Options": "nosniff", "Referrer-Policy": "no-referrer", "X-Frame-Options": "DENY",
	} {
		if got := resp.Header.Get(h); got != want {
			t.Errorf("%s = %q, want %q", h, got, want)
		}
	}
	csp := resp.Header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "default-src 'self'") || !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Errorf("Content-Security-Policy = %q", csp)
	}
	if resp.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Error("the server must not send CORS headers")
	}
	// The refusal replies carry them too.
	r2, _ := get(t, &http.Client{}, "http://"+s.Addr()+"/", nil)
	if r2.Header.Get("X-Frame-Options") != "DENY" {
		t.Error("an error reply is missing the security headers")
	}
}

func TestTokensAreRandomAndNotGuessable(t *testing.T) {
	s, _ := start(t)
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		u, err := s.NewURL()
		if err != nil {
			t.Fatal(err)
		}
		parsed, _ := url.Parse(u)
		tok := parsed.Query().Get("t")
		if len(tok) != 64 || seen[tok] {
			t.Fatalf("token %q is short or repeated", tok)
		}
		seen[tok] = true
	}
}

func TestCloseStopsServing(t *testing.T) {
	s, c := start(t)
	signIn(t, s, c)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if _, err := c.Get("http://" + s.Addr() + "/"); err == nil {
		t.Fatal("the server still answers after Close")
	}
}
