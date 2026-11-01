package uiapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	uiv1 "github.com/easydev-org/easyagent-sandbox-ext/gen/ui/v1"
	"github.com/easydev-org/easyagent-sandbox-ext/internal/config"
)

/**
 * THE ROUTE ITSELF, tested through a real ServeMux.
 *
 * Every assertion here is about the SHAPE of a request a browser makes, because that is the only shape
 * that ever reaches this code: a page loads, then asks for its own script, then opens a WebSocket —
 * none of which carries the signed query string that authorised the page.
 */

func newDesktopMux(t *testing.T, upstream *httptest.Server) (*http.ServeMux, *Service) {
	t.Helper()
	s := &Service{
		SignKey: []byte("test"),
		Sandboxes: func(_, tenant string) []config.Sandbox {
			if tenant != "alice" {
				return nil
			}
			return []config.Sandbox{{Name: "labwc", URL: upstream.URL, Token: "wt"}}
		},
	}
	mux := http.NewServeMux()
	s.RegisterDesktop(mux, "/ext/sandbox")
	return mux, s
}

func signedURL(t *testing.T, s *Service) string {
	t.Helper()
	ctx := context.WithValue(context.Background(), tenantKey{}, "alice")
	res, err := s.AuthorizeDesktop(ctx, &uiv1.AuthorizeDesktopRequest{Sandbox: "labwc"})
	if err != nil {
		t.Fatal(err)
	}
	return res.Url
}

// THE FIRST REQUEST: a signed query string, no cookie. It must be accepted and hand back a cookie.
func TestDesktopPageIsServedAndSetsACookie(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("UPSTREAM " + r.URL.Path))
	}))
	defer up.Close()
	mux, s := newDesktopMux(t, up)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, signedURL(t, s), nil))
	if w.Code != http.StatusOK {
		t.Fatalf("the signed page = %d, body=%q", w.Code, w.Body.String())
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("expected one cookie, got %d", len(cookies))
	}
	c := cookies[0]
	// A BROWSER DISCARDS SameSite=None WITHOUT Secure. Both or neither; a test that only checks the
	// name would pass against a cookie no browser keeps.
	if c.SameSite != http.SameSiteNoneMode || !c.Secure {
		t.Fatalf("cookie is SameSite=%v Secure=%v; a browser drops None without Secure", c.SameSite, c.Secure)
	}
	if c.Path != "/ext/sandbox/desktop/labwc/" {
		t.Fatalf("cookie path = %q, so a sub-resource would not carry it", c.Path)
	}
}

// THE SECOND REQUEST: a sub-resource, cookie only, no query. This is what every asset and the
// WebSocket handshake look like.
func TestDesktopSubresourceIsAuthorisedByCookieAlone(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("UPSTREAM " + r.URL.Path))
	}))
	defer up.Close()
	mux, s := newDesktopMux(t, up)

	// Get the cookie from the page request.
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, signedURL(t, s), nil))
	cookie := w.Result().Cookies()[0]

	for _, path := range []string{
		"/ext/sandbox/desktop/labwc/app.js",
		"/ext/sandbox/desktop/labwc/est/ws?audio=1",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.AddCookie(cookie)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		if rr.Code == http.StatusForbidden {
			t.Fatalf("%s was refused with a valid cookie: %q", path, rr.Body.String())
		}
	}
}

// THE STREAM GOES TO /desktop/est/ws, UNDER THE UI'S OWN MOUNT — NOT REWRITTEN.
//
// IT WAS REWRITTEN ONCE, to the worker's own /est/ws, and that endpoint speaks a DIFFERENT protocol:
// its own JSON hello and bare NAL units. easystream read the JSON as if it were packed est fields, so
// the page drew its whole toolbar and its canvas stayed one colour. Measured against the worker
// directly, /desktop/est/ws is a 101 with Sec-WebSocket-Protocol: est and streams binary frames — the
// page shows "live 1024x768".
func TestDesktopStreamStaysUnderTheDesktopMount(t *testing.T) {
	var got string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Path
		_, _ = w.Write([]byte("ok"))
	}))
	defer up.Close()
	mux, s := newDesktopMux(t, up)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, signedURL(t, s), nil))
	cookie := w.Result().Cookies()[0]

	req := httptest.NewRequest(http.MethodGet, "/ext/sandbox/desktop/labwc/est/ws?audio=1", nil)
	req.AddCookie(cookie)
	mux.ServeHTTP(httptest.NewRecorder(), req)

	if got != "/desktop/est/ws" {
		t.Fatalf("the stream reached the worker at %q, want /desktop/est/ws", got)
	}
}

/**
 * THE STREAM KEEPS ITS ORIGIN HEADER; THE UI'S FILES DO NOT.
 *
 * The worker's /est/ws is served by golang.org/x/net/websocket, whose Handler REFUSES a handshake with
 * no Origin — 403, before any of the worker's own code runs. This proxy used to strip Origin from every
 * request to satisfy the daemon's loopback check, so the UI's files arrived and its stream never did:
 * a panel that drew its whole toolbar, opened no socket, and said "websocket error" with nothing else
 * to go on.
 */
func TestDesktopStreamKeepsItsOriginHeader(t *testing.T) {
	seen := map[string]string{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen[r.URL.Path] = r.Header.Get("Origin")
		_, _ = w.Write([]byte("ok"))
	}))
	defer up.Close()
	mux, s := newDesktopMux(t, up)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, signedURL(t, s), nil))
	cookie := w.Result().Cookies()[0]

	for _, tc := range []struct{ path, wantOrig string }{
		{"/ext/sandbox/desktop/labwc/est/ws?audio=1", "https://console.example"},
		{"/ext/sandbox/desktop/labwc/app.js", ""},
	} {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		req.AddCookie(cookie)
		req.Header.Set("Origin", "https://console.example")
		mux.ServeHTTP(httptest.NewRecorder(), req)
	}

	if got := seen["/desktop/est/ws"]; got != "https://console.example" {
		t.Fatalf("the stream reached the worker with Origin=%q; its websocket handler refuses that", got)
	}
	if got, ok := seen["/desktop/app.js"]; !ok || got != "" {
		t.Fatalf("a UI file carried Origin=%q (present=%v); the daemon's check is loopback-only", got, ok)
	}
}

// A COOKIE FOR ONE MACHINE MUST NOT OPEN ANOTHER.
func TestDesktopCookieIsScopedToOneMachine(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer up.Close()
	s := &Service{
		SignKey: []byte("test"),
		Sandboxes: func(_, tenant string) []config.Sandbox {
			if tenant != "alice" {
				return nil
			}
			return []config.Sandbox{
				{Name: "labwc", URL: up.URL, Token: "wt"},
				{Name: "other", URL: up.URL, Token: "wt"},
			}
		},
	}
	mux := http.NewServeMux()
	s.RegisterDesktop(mux, "/ext/sandbox")

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, signedURL(t, s), nil))
	cookie := w.Result().Cookies()[0]

	// THE SAME COOKIE, A DIFFERENT MACHINE'S PATH.
	req := httptest.NewRequest(http.MethodGet, "/ext/sandbox/desktop/other/", nil)
	req.AddCookie(cookie)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("a cookie for labwc opened another machine: %d", rr.Code)
	}
}
