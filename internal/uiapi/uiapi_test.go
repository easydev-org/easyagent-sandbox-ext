package uiapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/easydev-org/easyagent-sandbox-ext/internal/config"
)

// A resolver that maps one token to one tenant, so a test can say who is asking.
type fakeIdentity struct {
	tenantOf map[string]string
	err      error
}

func (f fakeIdentity) Tenant(_ context.Context, bearer string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	if t, ok := f.tenantOf[bearer]; ok {
		return t, nil
	}
	return "", ErrUnauthenticated
}

func newHandler(id IdentityResolver, sandboxes func(string, string) []config.Sandbox) http.Handler {
	return Handler(Deps{Tenants: id, Sandboxes: sandboxes})
}

/**
 * Authentication is checked BEFORE anything is parsed.
 *
 * An unauthenticated caller must not be able to tell an empty deployment from a full one, so every
 * route answers 401 without looking at what was asked for.
 */
func TestUnauthenticatedIsRefused(t *testing.T) {
	h := newHandler(fakeIdentity{tenantOf: map[string]string{"good": "alice"}},
		func(string, string) []config.Sandbox {
			t.Fatal("the sandbox list must not be read for an unauthenticated caller")
			return nil
		})
	for _, path := range []string{
		"/sandboxes",
		"/jobs?sb=box",
		"/jobs/box/job-1/watch",
		"/sandboxes/box/screen",
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: got %d, want 401", path, rec.Code)
		}
	}
}

// A rejected token is 401. An agent that cannot be REACHED is 503 — a blip must not read as a bad
// credential, or every reader is logged out over a network fault.
func TestAgentUnreachableIsNotAnAuthFailure(t *testing.T) {
	h := newHandler(fakeIdentity{err: errors.New("dial tcp: connection refused")},
		func(string, string) []config.Sandbox { return nil })
	req := httptest.NewRequest(http.MethodGet, "/sandboxes", nil)
	// A token IS presented; the resolver simply cannot answer. Without this the request would be
	// refused for having no credential at all, which is a different case.
	req.Header.Set("Authorization", "Bearer some-token")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("got %d, want 503", rec.Code)
	}
}

// The tenant comes from the TOKEN and selects the sandbox list; a query parameter cannot reach
// another tenant's machines.
func TestTenantComesFromTheToken(t *testing.T) {
	var askedFor string
	h := newHandler(fakeIdentity{tenantOf: map[string]string{"alice-token": "alice"}},
		func(session, tenant string) []config.Sandbox {
			askedFor = tenant + "/" + session
			return nil
		})
	req := httptest.NewRequest(http.MethodGet, "/sandboxes?tenant=bob&session=s1", nil)
	req.Header.Set("Authorization", "Bearer alice-token")
	h.ServeHTTP(httptest.NewRecorder(), req)

	if askedFor != "alice/s1" {
		t.Errorf("listed %q, want the TOKEN's tenant and the query's session", askedFor)
	}
}

// A sandbox that is not the caller's answers 404, not 403: the caller may know this name is not
// theirs without learning that it exists elsewhere.
func TestUnknownSandboxIsNotFound(t *testing.T) {
	h := newHandler(fakeIdentity{tenantOf: map[string]string{"good": "alice"}},
		func(string, string) []config.Sandbox { return []config.Sandbox{{Name: "mine", URL: "http://w"}} })
	req := httptest.NewRequest(http.MethodGet, "/jobs?sb=theirs", nil)
	req.Header.Set("Authorization", "Bearer good")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("got %d, want 404", rec.Code)
	}
}

// A worker URL is http://; a WebSocket handshake needs ws://. Built wrong it becomes a plain GET that
// the worker answers as 404, which reads as "no screen" rather than "wrong scheme".
func TestEstURLRewritesTheScheme(t *testing.T) {
	cases := map[string]string{
		"http://worker:8080":  "ws://worker:8080/est/ws",
		"https://worker":      "wss://worker/est/ws",
		"http://worker/":      "ws://worker/est/ws",
		"ws://worker":         "ws://worker/est/ws",
		"http://worker:8080/": "ws://worker:8080/est/ws",
	}
	for in, want := range cases {
		got, err := estURL(in)
		if err != nil {
			t.Errorf("%s: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("%s -> %s, want %s", in, got, want)
		}
	}
}

func TestEstURLRejectsAnUnknownScheme(t *testing.T) {
	if _, err := estURL("ftp://worker"); err == nil {
		t.Error("expected an error for an unsupported scheme")
	}
}

// SSE must split on newlines: a data field cannot contain a raw newline, and a chunk of output is
// usually several lines.
func TestSSESplitsLines(t *testing.T) {
	rec := httptest.NewRecorder()
	sse(rec, "output", "one\ntwo")
	want := "event: output\ndata: one\n\nevent: output\ndata: two\n\n"
	if rec.Body.String() != want {
		t.Errorf("got %q, want %q", rec.Body.String(), want)
	}
}
