package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Stellatinus/Stellatinus-Installer/internal/manifest"
)

func handler(t *testing.T) (http.Handler, chan struct{}) {
	t.Helper()
	m, err := manifest.Load("../../examples/haedus.json")
	if err != nil {
		t.Fatal(err)
	}
	quit := make(chan struct{}, 1)
	h, err := newHandler(m, func() { quit <- struct{}{} })
	if err != nil {
		t.Fatal(err)
	}
	return h, quit
}

func TestThePageIsServedAndNeedsNothingFromOutside(t *testing.T) {
	h, _ := handler(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	body, _ := io.ReadAll(rec.Result().Body)
	if rec.Code != http.StatusOK || !strings.Contains(string(body), "Checking that everything is in order") {
		t.Fatalf("index: %d", rec.Code)
	}
	// The server's policy is default-src 'self': a page that loads anything
	// from another address would show up blank under it.
	for _, bad := range []string{"http://", "https://", "//cdn", "@import", "<style", "style=", "onclick=", "<script>"} {
		if strings.Contains(string(body), bad) {
			t.Errorf("the page contains %q, which the server's Content-Security-Policy would block or which loads from outside", bad)
		}
	}
	for _, asset := range []string{"/app.css", "/app.js"} {
		r := httptest.NewRecorder()
		h.ServeHTTP(r, httptest.NewRequest(http.MethodGet, asset, nil))
		if r.Code != http.StatusOK || r.Body.Len() == 0 {
			t.Errorf("%s: %d", asset, r.Code)
		}
	}
}

func TestTheManifestEndpointCarriesTheAccentPalettes(t *testing.T) {
	h, _ := handler(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/manifest", nil))
	var got struct {
		Name     string
		Palettes map[string]map[string]string
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Name != "Haedus" || got.Palettes["light"]["ink"] == "" || got.Palettes["dark"]["fill"] == "" {
		t.Fatalf("%+v", got)
	}
}

func TestTheChecksEndpointRunsTheEngine(t *testing.T) {
	h, _ := handler(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/checks", nil))
	var res []struct{ ID, Status string }
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil || len(res) == 0 {
		t.Fatalf("checks: %v %s", err, rec.Body.String())
	}
	if res[0].ID != "privileges" {
		t.Fatalf("the first check is %q", res[0].ID)
	}
}

func TestQuitCallsBack(t *testing.T) {
	h, quit := handler(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/quit", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("%d", rec.Code)
	}
	select {
	case <-quit:
	case <-time.After(2 * time.Second):
		t.Fatal("the quit callback was not called")
	}
}
