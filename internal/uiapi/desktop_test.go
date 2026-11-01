package uiapi

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	uiv1 "github.com/easydev-org/easyagent-sandbox-ext/gen/ui/v1"
	"github.com/easydev-org/easyagent-sandbox-ext/internal/config"
)

/**
 * The signature is the whole permission model for the desktop proxy, because an iframe navigation
 * cannot carry an Authorization header. Every test here is a way it could be bypassed.
 */

func svcForDesktop() *Service {
	return &Service{SignKey: []byte("test-key-not-a-real-secret")}
}

// link builds the `t=` value AuthorizeDesktop would produce.
func link(s *Service, tenant, sandbox string, exp int64) string {
	body := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("%s:%s:%d", tenant, sandbox, exp)))
	return body + "." + s.signDesktop(tenant, sandbox, exp)
}

func TestVerifyAcceptsAFreshLink(t *testing.T) {
	s := svcForDesktop()
	exp := time.Now().Add(time.Minute).Unix()
	got, err := s.verifyDesktop("labwc", link(s, "alice", "labwc", exp), time.Now())
	if err != nil {
		t.Fatalf("a valid link was rejected: %v", err)
	}
	if got.tenant != "alice" || got.sandbox != "labwc" {
		t.Fatalf("claims = %+v", got)
	}
}

func TestVerifyRejectsAnExpiredLink(t *testing.T) {
	s := svcForDesktop()
	exp := time.Now().Add(-time.Second).Unix()
	if _, err := s.verifyDesktop("labwc", link(s, "alice", "labwc", exp), time.Now()); err == nil {
		// A LINK THAT OUTLIVES ITS EXPIRY is a leaked screen with no end.
		t.Fatal("an expired link must be refused")
	}
}

func TestVerifyRejectsAnotherSandbox(t *testing.T) {
	s := svcForDesktop()
	exp := time.Now().Add(time.Minute).Unix()
	// A VALID LINK FOR ONE MACHINE, USED ON ANOTHER: the path and the signed body must agree, or
	// editing the URL would move a reader to a machine the signature never named.
	if _, err := s.verifyDesktop("other", link(s, "alice", "labwc", exp), time.Now()); err == nil {
		t.Fatal("a link for one sandbox must not open another")
	}
}

func TestVerifyRejectsAForgedSignature(t *testing.T) {
	s := svcForDesktop()
	exp := time.Now().Add(time.Minute).Unix()
	body := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("alice:labwc:%d", exp)))
	// HANDPICKED TENANT, INVENTED SIGNATURE: the body is readable, so this is the attack the signature
	// exists to stop.
	if _, err := s.verifyDesktop("labwc", body+".deadbeef", time.Now()); err == nil {
		t.Fatal("a forged signature must be refused")
	}
}

func TestVerifyRejectsAnotherTenantsLink(t *testing.T) {
	s := svcForDesktop()
	exp := time.Now().Add(time.Minute).Unix()
	// A LINK SIGNED FOR BOB'S MACHINE, presented as if it were Alice's. The sandbox name matches, so
	// only the tenant inside the signed body distinguishes them — which is why the tenant is signed.
	linkForBob := link(s, "bob", "labwc", exp)
	got, err := s.verifyDesktop("labwc", linkForBob, time.Now())
	if err != nil {
		t.Fatalf("the link itself is valid: %v", err)
	}
	if got.tenant != "bob" {
		// The proxy then looks the machine up in BOB'S config, so a forged tenant cannot reach Alice's
		// machine: the isolation is the scoped lookup, and this asserts the claim that drives it.
		t.Fatalf("the tenant must come from the signature, got %q", got.tenant)
	}
}

func TestVerifyRejectsGarbage(t *testing.T) {
	s := svcForDesktop()
	for _, bad := range []string{"", "no-dot", "a.b", strings.Repeat("x", 400)} {
		if _, err := s.verifyDesktop("labwc", bad, time.Now()); err == nil {
			t.Fatalf("%q must be refused", bad)
		}
	}
}

func TestSignaturesDependOnTheKey(t *testing.T) {
	a := &Service{SignKey: []byte("one")}
	b := &Service{SignKey: []byte("two")}
	exp := time.Now().Unix()
	// TWO PROCESSES WITH DIFFERENT KEYS MUST NOT ACCEPT EACH OTHER'S LINKS.
	if a.signDesktop("alice", "labwc", exp) == b.signDesktop("alice", "labwc", exp) {
		t.Fatal("different keys produced the same signature")
	}
}

func TestDefaultKeyIsStable(t *testing.T) {
	// NO CONFIGURED KEY: a restarted process must not invalidate every open panel, so the derived key
	// has to be the same one.
	a := &Service{}
	b := &Service{}
	exp := time.Now().Unix()
	if a.signDesktop("alice", "labwc", exp) != b.signDesktop("alice", "labwc", exp) {
		t.Fatal("the derived key is not stable across instances")
	}
}

/**
 * THE URL AuthorizeDesktop RETURNS MUST BE A PATH THE PROXY ACCEPTS.
 *
 * THESE TWO DISAGREED ONCE, and the failure was silent: the RPC returned
 * /ext/sandbox/desktop/<name> while the proxy was mounted under /ext/sandbox/api/desktop/<name>, so
 * every screen was a 404. The URL looked correct in a browser, in the network log and in the panel's
 * own code — the mismatch only existed between two lines in different files.
 *
 * This asserts the SHAPE agreement, without needing a running proxy: whatever prefix the RPC builds,
 * the proxy must be able to claim it.
 */
func TestAuthorizeURLMatchesTheProxyMount(t *testing.T) {
	const base = "/ext/sandbox"

	s := &Service{
		SignKey: []byte("test"),
		Sandboxes: func(_, tenant string) []config.Sandbox {
			if tenant != "alice" {
				return nil
			}
			return []config.Sandbox{{Name: "labwc", URL: "http://worker:48080", Token: "t"}}
		},
	}
	ctx := context.WithValue(context.Background(), tenantKey{}, "alice")
	res, err := s.AuthorizeDesktop(ctx, &uiv1.AuthorizeDesktopRequest{Sandbox: "labwc"})
	if err != nil {
		t.Fatal(err)
	}

	// THE PROXY OWNS `<base>/desktop/`. The URL must start with exactly that, or the request reaches
	// this extension and finds no handler.
	want := base + "/desktop/"
	if !strings.HasPrefix(res.Url, want) {
		t.Fatalf("AuthorizeDesktop returned %q, which the proxy at %q cannot serve", res.Url, want+"<name>")
	}

	// AND THE TOKEN IN IT MUST VERIFY, so the two halves of this feature agree end to end.
	token := res.Url[strings.Index(res.Url, "t=")+2:]
	if i := strings.Index(token, "&"); i >= 0 {
		token = token[:i]
	}
	if unescaped, uerr := url.QueryUnescape(token); uerr == nil {
		token = unescaped
	}
	claims, verr := s.verifyDesktop("labwc", token, time.Now())
	if verr != nil {
		t.Fatalf("the URL's own token does not verify: %v", verr)
	}
	if claims.tenant != "alice" {
		t.Fatalf("claims = %+v", claims)
	}
}

/**
 * THE URL MUST END AT A SLASH BEFORE THE QUERY.
 *
 * easystream's page loads its script as `<script src="app.js">` — RELATIVE — and a browser resolves
 * that against everything up to the LAST slash. Without one, the base was
 * /ext/sandbox/desktop/ instead of /ext/sandbox/desktop/labwc/, so the page fetched
 * /ext/sandbox/desktop/app.js: a path with no machine in it, refused with 403, and the panel rendered a
 * complete UI with no script behind it.
 *
 * THE FAILURE LOOKED LIKE A WORKING PAGE — bars, buttons, a keyboard, and a canvas that never filled.
 */
func TestAuthorizeURLKeepsRelativeAssetsUnderTheMachine(t *testing.T) {
	s := &Service{
		SignKey: []byte("test"),
		Sandboxes: func(_, tenant string) []config.Sandbox {
			if tenant != "alice" {
				return nil
			}
			return []config.Sandbox{{Name: "labwc", URL: "http://worker:48080", Token: "t"}}
		},
	}
	ctx := context.WithValue(context.Background(), tenantKey{}, "alice")
	res, err := s.AuthorizeDesktop(ctx, &uiv1.AuthorizeDesktopRequest{Sandbox: "labwc"})
	if err != nil {
		t.Fatal(err)
	}

	// Resolve a relative asset the way a browser does, and assert it stays inside this machine's path.
	base, berr := url.Parse("http://host" + res.Url)
	if berr != nil {
		t.Fatal(berr)
	}
	asset, aerr := base.Parse("app.js")
	if aerr != nil {
		t.Fatal(aerr)
	}
	want := "/ext/sandbox/desktop/labwc/app.js"
	if asset.Path != want {
		t.Fatalf("a relative app.js resolved to %q, want %q — the URL needs a trailing slash before ?t=", asset.Path, want)
	}
}
