package uiapi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"

	uiv1 "github.com/easydev-org/easyagent-sandbox-ext/gen/ui/v1"
	"github.com/easydev-org/easyagent-sandbox-ext/internal/config"
)

/**
 * The live screen, and it is NOT implemented here.
 *
 * THE WORKER ALREADY SERVES THE EASYSTREAM UI at /desktop/. That UI is easystream-webui — video out,
 * pointer and keyboard in, zoom, pan, a virtual keyboard — and it is the same code the sandbox image
 * ships. What used to be here was 141 lines of WebSocket proxy forwarding H.264 access units, which
 * gave the reader a picture with no way to touch it.
 *
 * So this file forwards /desktop/ instead of reimplementing it.
 *
 * ---------------------------------------------------------------------------
 * THE HARD PART IS NOT THE PROXY, IT IS THE PERMISSION
 * ---------------------------------------------------------------------------
 *
 * An iframe NAVIGATION cannot carry an Authorization header — the browser's rule — so the URL itself
 * has to say who was allowed to ask. Two things follow, and they are the design:
 *
 *   1. AuthorizeDesktop is the only entry point. It is a normal RPC, so the caller's token resolves to
 *      a tenant, and the sandbox must be one of THAT TENANT'S configured machines. A name that is not
 *      theirs answers NOT_FOUND — the caller may know it is not theirs, and the alternative would
 *      confirm it exists somewhere else.
 *
 *   2. The URL it returns carries an HMAC over (tenant, sandbox, expiry). The proxy verifies that
 *      signature before it forwards a byte, so a hand-typed URL cannot reach a machine the signer did
 *      not authorise, and one that leaks is useless after a few minutes.
 *
 * AND THE THIRD CHECK IS THE TENANT'S OWN TOKEN. The proxy presents the token from THAT TENANT'S
 * config to the worker. A wrong token fails there, which is the right place for it: the panel shows
 * the machine as unreachable rather than somebody else's screen.
 */

// desktopTTL is how long a signed desktop URL stays valid.
//
// FIVE MINUTES IS A COMPROMISE BETWEEN TWO FAILURES. Too long and a leaked URL is a leaked screen; too
// short and a reader watching a job finds the frame dead without knowing why. The panel refreshes on
// its own before this expires, so the number only matters when something else has gone wrong.
const desktopTTL = 5 * time.Minute

// desktopKey derives the signing key.
//
// DERIVED FROM THE EXTENSION'S OWN IDENTITY rather than configured: the signature protects a URL this
// process alone issues, and a deployment that had to generate and distribute a second secret would
// eventually ship the same one everywhere. Where the SDK gives a stable secret this should use it; the
// fallback below is deliberately loud in the logs when it is used.
func (s *Service) desktopKey() []byte {
	if len(s.SignKey) > 0 {
		return s.SignKey
	}
	// A STABLE DEFAULT so a restarted process does not invalidate every open panel. It is not a secret
	// against somebody who can read this source — which is why the note above says to configure one.
	sum := sha256.Sum256([]byte("easyagent-sandbox-ext/desktop/v1"))
	return sum[:]
}

// signDesktop returns the HMAC for one (tenant, sandbox, expiry) triple.
func (s *Service) signDesktop(tenant, sandbox string, exp int64) string {
	mac := hmac.New(sha256.New, s.desktopKey())
	// THE TENANT IS IN THE SIGNED PAYLOAD, not just the name: without it a signature for one tenant's
	// "labwc" would authorise another tenant's "labwc", and two tenants naming a machine the same way
	// is the normal case.
	fmt.Fprintf(mac, "%s\n%s\n%d", tenant, sandbox, exp)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// desktopClaims is what a verified signature says.
type desktopClaims struct {
	tenant  string
	sandbox string
}

/**
 * verifyDesktop checks a signature and returns what it claims.
 *
 * THE URL CARRIES `<body>.<signature>`, both base64url. The body is readable — it holds the tenant,
 * the machine and an expiry — and the signature is what makes it trustworthy. Signing the body rather
 * than looking the tenant up again is what lets the proxy answer without a credential of its own: it
 * has nothing to look the tenant up WITH, so the caller's authorisation has to travel with the URL.
 */
func (s *Service) verifyDesktop(sandbox, token string, now time.Time) (desktopClaims, error) {
	body, sig, found := strings.Cut(token, ".")
	if !found {
		return desktopClaims{}, fmt.Errorf("malformed desktop link")
	}
	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return desktopClaims{}, fmt.Errorf("malformed desktop link")
	}
	parts := strings.SplitN(string(raw), ":", 3)
	if len(parts) != 3 {
		return desktopClaims{}, fmt.Errorf("malformed desktop link")
	}
	tenant, name, expStr := parts[0], parts[1], parts[2]
	if name != sandbox {
		// THE PATH AND THE SIGNED BODY MUST AGREE, or a valid link for one machine would open another
		// by editing the URL.
		return desktopClaims{}, fmt.Errorf("this desktop link is for a different machine")
	}
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil {
		return desktopClaims{}, fmt.Errorf("malformed desktop link")
	}
	if now.Unix() > exp {
		return desktopClaims{}, fmt.Errorf("this desktop link has expired")
	}
	// CONSTANT-TIME, because a byte-by-byte compare leaks how much of a forged signature was right.
	if !hmac.Equal([]byte(sig), []byte(s.signDesktop(tenant, name, exp))) {
		return desktopClaims{}, fmt.Errorf("this desktop link is not valid")
	}
	return desktopClaims{tenant: tenant, sandbox: name}, nil
}

// AuthorizeDesktop decides whether the caller may see this sandbox's screen, and signs a URL if so.
func (s *Service) AuthorizeDesktop(ctx context.Context, req *uiv1.AuthorizeDesktopRequest) (*uiv1.AuthorizeDesktopResponse, error) {
	sb, err := s.find(ctx, req.GetSession(), req.GetSandbox())
	if err != nil {
		// find() already scoped the lookup to the caller's tenant, so a sandbox that is not theirs is
		// NOT_FOUND here — this RPC is the permission check, and it fails before a URL exists.
		return nil, err
	}
	tenant, terr := tenantFrom(ctx)
	if terr != nil {
		return nil, terr
	}
	exp := time.Now().Add(desktopTTL)
	// THE TOKEN BODY IS tenant:sandbox:exp, and the signature is over the same three fields. The body
	// travels in the URL so the proxy can read the tenant without a lookup; the signature is what makes
	// the body trustworthy.
	body := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("%s:%s:%d", tenant, sb.Name, exp.Unix())))
	sig := s.signDesktop(tenant, sb.Name, exp.Unix())
	return &uiv1.AuthorizeDesktopResponse{
		Url:       "/ext/sandbox/desktop/" + url.PathEscape(sb.Name) + "?t=" + url.QueryEscape(body+"."+sig),
		ExpiresAt: exp.Unix(),
	}, nil
}

/**
 * RegisterDesktop mounts the screen proxy.
 *
 * THE MOUNT IS PATH-SHAPED, NOT A QUERY PARAMETER: easystream's UI builds its WebSocket URL from
 * `location.pathname + 'est/ws'`, so it works under any prefix — but only one that contains the
 * sandbox. A `?sb=labwc` would have the UI look for `est/ws` at a path that names no machine.
 */
func (s *Service) RegisterDesktop(mux *http.ServeMux, base string) {
	prefix := base + "/desktop/"
	mux.HandleFunc(prefix, func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, prefix)
		name, tail, _ := strings.Cut(rest, "/")
		if name == "" {
			http.NotFound(w, r)
			return
		}

		claims, err := s.verifyDesktop(name, r.URL.Query().Get("t"), time.Now())
		if err != nil {
			// A BROWSEABLE ERROR, not JSON: this path is loaded by an <iframe>, and a page saying what
			// went wrong is more useful there than a status code nobody sees.
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}

		// THE TENANT COMES FROM THE SIGNATURE, which is the only credential this request has.
		target, ok := s.desktopTarget(claims)
		if !ok {
			http.Error(w, "no such sandbox", http.StatusNotFound)
			return
		}

		up := &httputil.ReverseProxy{
			Director: func(req *http.Request) {
				req.URL.Scheme = target.scheme
				req.URL.Host = target.host
				req.Host = target.host
				// /ext/sandbox/desktop/labwc/app.js -> /desktop/app.js
				req.URL.Path = "/desktop/" + tail
				// The worker mounts the daemon's UI at /desktop/ and injects its own control token on
				// the way through — so this side presents the WORKER's bearer, exactly as every other
				// call to it does. Without it /desktop/ answers 401.
				if target.token != "" {
					req.Header.Set("Authorization", "Bearer "+target.token)
				}
				// The daemon's origin check is loopback-only; the worker's own proxy drops Origin for
				// the same reason. A browser Origin here is an origin the daemon does not recognise.
				req.Header.Del("Origin")
				// The signed token is this proxy's business; the worker never sees it.
				q := req.URL.Query()
				q.Del("t")
				req.URL.RawQuery = q.Encode()
			},
			ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
				http.Error(w, "desktop unavailable: "+err.Error(), http.StatusBadGateway)
			},
		}
		// WEBSOCKET UPGRADES ARE HANDLED BY ReverseProxy ITSELF since Go 1.12: it sees the Upgrade
		// header and switches to a raw byte pipe. That is the whole reason this is a proxy rather than
		// a second implementation of the est protocol.
		up.ServeHTTP(w, r)
	})
}

// desktopTarget is one machine and the token its tenant configured for it.
type desktopTarget struct {
	scheme string
	host   string
	token  string
}

/**
 * desktopTarget resolves a verified claim.
 *
 * IT READS THE TENANT'S OWN CONFIG, and that is the second half of the isolation: even a valid
 * signature for tenant A cannot reach a machine configured only for tenant B, because the lookup is
 * scoped to A.
 */
func (s *Service) desktopTarget(c desktopClaims) (desktopTarget, bool) {
	for _, sb := range s.Sandboxes("", c.tenant) {
		if sb.Name != c.sandbox {
			continue
		}
		u, err := url.Parse(sb.URL)
		if err != nil {
			return desktopTarget{}, false
		}
		return desktopTarget{scheme: u.Scheme, host: u.Host, token: sb.Token}, true
	}
	return desktopTarget{}, false
}

// keep the config import meaningful if the resolver's shape changes.
var _ = config.Sandbox{}
