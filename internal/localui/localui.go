// Package localui serves the setup window's web interface on 127.0.0.1 for
// the one person who opens it. Any web page the person visits, and any other
// program on the machine, can reach 127.0.0.1, so the server does not trust a
// request just because it arrives: the address carries a single-use token that
// is exchanged for a session cookie, the Host header must be the server's own
// (which defeats DNS rebinding), a request that changes something must come
// from the page itself, and the replies tell the browser not to cache, frame
// or share anything. It does not draw the window, choose a browser, or decide
// what the interface does.
package localui

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"
)

const (
	cookieName = "stellatinus-ui"
	maxBody    = 1 << 20
)

// Server is a running local interface.
type Server struct {
	ln   net.Listener
	srv  *http.Server
	addr string

	mu       sync.Mutex
	tokens   map[string]bool // unused one-time tokens
	sessions map[string]bool
}

// New starts serving next on a random port of 127.0.0.1. Serve must be called
// to handle requests.
func New(next http.Handler) (*Server, error) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s := &Server{ln: ln, addr: ln.Addr().String(), tokens: map[string]bool{}, sessions: map[string]bool{}}
	s.srv = &http.Server{
		Handler:           s.guard(next),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	return s, nil
}

// Addr is the address the server listens on, always 127.0.0.1:port.
func (s *Server) Addr() string { return s.addr }

// NewURL returns an address that opens the interface once: the token in it
// works for one request and is then useless, so an address left in a browser's
// history or a log cannot be used again.
func (s *Server) NewURL() (string, error) {
	t, err := randomHex(32)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	s.tokens[t] = true
	s.mu.Unlock()
	return "http://" + s.addr + "/?t=" + t, nil
}

// Serve handles requests until Close or the context ends.
func (s *Server) Serve(ctx context.Context) error {
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.srv.Shutdown(sctx)
	}()
	err := s.srv.Serve(s.ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Close stops the server.
func (s *Server) Close() error { return s.srv.Close() }

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (s *Server) sessionOK(r *http.Request) bool {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for known := range s.sessions {
		if subtle.ConstantTimeCompare([]byte(known), []byte(c.Value)) == 1 {
			return true
		}
	}
	return false
}

// takeToken uses up a token, in constant time per candidate.
func (s *Server) takeToken(candidate string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	found := ""
	for t := range s.tokens {
		if subtle.ConstantTimeCompare([]byte(t), []byte(candidate)) == 1 {
			found = t
		}
	}
	if found == "" {
		return false
	}
	delete(s.tokens, found)
	return true
}

func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Cache-Control", "no-store")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")

		// DNS rebinding: a page on another name that resolves to 127.0.0.1
		// sends its own name in Host. Only the server's own address is served.
		if r.Host != s.addr {
			http.Error(w, "unexpected host", http.StatusForbidden)
			return
		}

		// The one-time address: exchange the token for a session cookie, then
		// go to the same page without the token in it.
		if t := r.URL.Query().Get("t"); t != "" && r.Method == http.MethodGet && r.URL.Path == "/" {
			if !s.takeToken(t) {
				http.Error(w, "this address was already used or is not valid", http.StatusUnauthorized)
				return
			}
			session, err := randomHex(32)
			if err != nil {
				http.Error(w, "no randomness", http.StatusInternalServerError)
				return
			}
			s.mu.Lock()
			s.sessions[session] = true
			s.mu.Unlock()
			http.SetCookie(w, &http.Cookie{Name: cookieName, Value: session, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
			http.Redirect(w, r, "/", http.StatusFound)
			return
		}

		if !s.sessionOK(r) {
			http.Error(w, "not signed in", http.StatusUnauthorized)
			return
		}

		// A request that changes something must come from the page itself.
		switch r.Method {
		case http.MethodGet, http.MethodHead:
		default:
			if r.Header.Get("Origin") != "http://"+s.addr {
				http.Error(w, "unexpected origin", http.StatusForbidden)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		}
		next.ServeHTTP(w, r)
	})
}

// String describes the server for logs.
func (s *Server) String() string { return fmt.Sprintf("local interface on %s", s.addr) }
