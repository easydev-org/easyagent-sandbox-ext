package uiapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// AgentIdentity resolves a bearer token to its tenant by asking the agent.
//
// WHY THE AGENT: the token is a row in the agent's database, and the token -> tenant mapping lives
// there. An extension has no table and no key, so it asks the component that issued the credential.
// `AgentService.GetIdentity` already answers exactly this question.
//
// The SAME contract the Go extensions' UI packages use; a panel mounted from one extension must not
// have a weaker notion of identity than one mounted from another.
type AgentIdentity struct {
	// AgentURL is the agent's base URL, e.g. http://agent.dev.svc.cluster.local
	AgentURL string
	Client   *http.Client
}

// Tenant resolves a bearer token to its tenant id.
func (a *AgentIdentity) Tenant(ctx context.Context, bearer string) (string, error) {
	if strings.TrimSpace(bearer) == "" {
		return "", ErrUnauthenticated
	}
	if a.AgentURL == "" {
		return "", errors.New("identity: no agent URL configured")
	}
	cl := a.Client
	if cl == nil {
		cl = &http.Client{Timeout: 5 * time.Second}
	}

	body := strings.NewReader("{}")
	req, err := http.NewRequestWithContext(
		ctx, http.MethodPost,
		strings.TrimSuffix(a.AgentURL, "/")+"/agent.v1.AgentService/GetIdentity",
		body,
	)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(bearer))

	res, err := cl.Do(req)
	if err != nil {
		// Unreachable is NOT unauthenticated: a brief outage must not read as a bad credential, or
		// every reader is logged out with a misleading message.
		return "", fmt.Errorf("identity: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<16))

	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
		return "", ErrUnauthenticated
	}
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("identity: agent answered %d: %s", res.StatusCode, bytes.TrimSpace(raw))
	}

	var out struct {
		Tenant string `json:"tenant"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("identity: unreadable answer: %w", err)
	}
	if out.Tenant == "" {
		return "", ErrUnauthenticated
	}
	return out.Tenant, nil
}
