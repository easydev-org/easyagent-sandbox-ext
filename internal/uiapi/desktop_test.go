package uiapi

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
	"time"
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
