// Package uiapi is the browser panel's data API.
//
// WHY THIS PACKAGE EXISTS AT ALL: the extension has never had an HTTP surface. It speaks the bus and
// calls each worker's Connect API, which is all a TOOL needs — but a panel needs a stable, cached,
// same-origin URL that a browser can open, and the browser cannot reach a worker's ClusterIP.
//
// So this package does three things and nothing else:
//
//	GET /api/sandboxes                      what sandboxes are configured, and are they alive
//	GET /api/jobs?sb=<name>                 the jobs one sandbox knows about
//	GET /api/jobs/<sb>/<id>/watch           SSE, forwarding the sandbox's own job stream
//	GET /api/sandboxes/<name>/screen        WebSocket, forwarding the sandbox's H.264 screen
//
// The two streaming routes are PROXIES, not reimplementations: easysh already serves job output as
// Server-Sent Events and easyworker already serves the screen as H.264 over one WebSocket. Rebuilding
// either here would be a second implementation to keep in step, and the point of this layer is only
// that the browser can reach them.
package uiapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/easydev-org/easyagent-sandbox-ext/internal/config"
	"github.com/easydev-org/easyagent-sandbox-ext/internal/sandbox"
)

// Deps is what the panel needs from the extension.
type Deps struct {
	// Sandboxes returns the configured sandboxes for one (session, tenant).
	//
	// A function rather than a snapshot because the list comes from extension CONFIG, which can change
	// while the process runs: a panel that cached its list at boot would keep showing a sandbox that
	// was removed, and never show one that was added.
	Sandboxes func(session, tenant string) []config.Sandbox
	// Tenants resolves a bearer token to its tenant. The panel must never trust a query parameter for
	// this: naming a tenant would read somebody else's machines.
	Tenants IdentityResolver
	// Cache builds the Connect client for one sandbox.
	Cache *sandbox.ClientCache
	// HTTP is used for the two proxy routes. Optional; a default is used when nil.
	HTTP *http.Client
}

// IdentityResolver turns a bearer token into the tenant it belongs to.
type IdentityResolver interface {
	Tenant(ctx context.Context, bearer string) (string, error)
}

// ErrUnauthenticated means the caller presented no usable bearer.
var ErrUnauthenticated = errors.New("a bearer token is required")

/**
 * Handler returns the panel's mux, mounted under `prefix` (e.g. "/ext/sandbox/api").
 *
 * The prefix is PREPENDED to every route rather than stripped by the caller: `MountUI` hands the
 * handler the request it received, path intact, so a handler that registered a bare "/sandboxes"
 * would never match "/ext/sandbox/api/sandboxes" and every call would 404. Registering the full path
 * is also how the GitHub extension is wired, so the two behave the same.
 */
func Handler(prefix string, d Deps) http.Handler {
	h := &handler{d: d}
	h.prefix = strings.TrimSuffix(prefix, "/")
	p := h.prefix + "/"
	mux := http.NewServeMux()
	mux.HandleFunc(p+"sandboxes", h.sandboxes)
	mux.HandleFunc(p+"sandboxes/", h.sandboxSub)
	mux.HandleFunc(p+"jobs", h.jobs)
	mux.HandleFunc(p+"jobs/", h.jobSub)
	return mux
}

type handler struct {
	d Deps
	// prefix is the mount point, kept so a sub-route can strip it from the request path.
	prefix string
}

// scope authenticates the request and reads the (session, tenant) it may see.
//
// Authentication comes first, always: an unauthenticated caller must not learn which sandboxes exist.
// The tenant comes from the TOKEN and the session from the query — the session only selects among a
// tenant's own sandboxes, so it can narrow but never widen.
func (h *handler) scope(w http.ResponseWriter, r *http.Request) (session, tenant string, ok bool) {
	bearer := bearerOf(r)
	if bearer == "" {
		writeErr(w, http.StatusUnauthorized, ErrUnauthenticated.Error())
		return "", "", false
	}
	t, err := h.d.Tenants.Tenant(r.Context(), bearer)
	if err != nil {
		// A failure to REACH the agent is not an authentication failure: answering 401 would log the
		// reader out over a momentary blip.
		status := http.StatusServiceUnavailable
		if errors.Is(err, ErrUnauthenticated) {
			status = http.StatusUnauthorized
		}
		writeErr(w, status, err.Error())
		return "", "", false
	}
	return r.URL.Query().Get("session"), t, true
}

// findSandbox resolves the named sandbox within the caller's scope, or writes an error.
func (h *handler) findSandbox(w http.ResponseWriter, r *http.Request, name string) (config.Sandbox, bool) {
	session, tenant, ok := h.scope(w, r)
	if !ok {
		return config.Sandbox{}, false
	}
	for _, s := range h.d.Sandboxes(session, tenant) {
		if s.Name == name {
			return s, true
		}
	}
	// 404 rather than 403: the caller is allowed to know that THIS NAME is not one of theirs, and the
	// alternative would tell them it exists somewhere else.
	writeErr(w, http.StatusNotFound, "no such sandbox")
	return config.Sandbox{}, false
}

func bearerOf(r *http.Request) string {
	v := r.Header.Get("Authorization")
	if len(v) > 7 && strings.EqualFold(v[:7], "bearer ") {
		return strings.TrimSpace(v[7:])
	}
	return ""
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// copyFlush streams src to dst, flushing after every write so a Server-Sent Events stream is delivered
// as it arrives instead of being buffered until the connection closes.
//
// http.ResponseWriter buffers by default, and without this the whole point of SSE — seeing lines as
// they are produced — is lost to a single write at the end.
func copyFlush(dst http.ResponseWriter, src io.Reader) error {
	flusher, _ := dst.(http.Flusher)
	buf := make([]byte, 4096)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return werr
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}
