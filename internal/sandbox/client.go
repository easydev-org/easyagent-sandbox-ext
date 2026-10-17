// Package sandbox builds bearer-authenticated worker.v1 clients (the easyworker
// Connect contract) and caches them per (url, token). It is the Go twin of the
// worker-extension's client.ts.
package sandbox

import (
	"context"
	"net/http"
	"strings"
	"sync"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"

	workerv1connect "github.com/easydev-org/easyagent-sandbox-ext/gen/worker/v1/workerv1connect"
)

// Endpoint is the address + credential an operation runs against.
type Endpoint struct {
	// URL is the easyworker base URL, e.g. http://worker:80.
	URL string
	// Token is the worker bearer token (empty only when auth is off).
	Token string
}

// normalizeURL trims trailing slashes so baseURL never ends in "/".
func normalizeURL(raw string) string {
	s := strings.TrimSpace(raw)
	for strings.HasSuffix(s, "/") {
		s = s[:len(s)-1]
	}
	return s
}

// bearerInterceptor adds `Authorization: Bearer <token>` to every call (the
// connect-go v2 client-interceptor shape, matching easyworker's client.Bearer).
func bearerInterceptor(token string) connect.ClientInterceptor {
	return func(next connect.ClientFunc) connect.ClientFunc {
		return func(ctx context.Context, spec connect.Spec) (connect.ClientStream, error) {
			if token != "" {
				var info *connect.CallInfo
				ctx, info = connect.NewClientContext(ctx)
				info.RequestHeader().Set("Authorization", "Bearer "+token)
			}
			return next(ctx, spec)
		}
	}
}

// WorkerClient is a worker.v1 Connect client.
type WorkerClient = workerv1connect.WorkerServiceClient

// ComputerClient is the worker-native GUI-automation client.
type ComputerClient = workerv1connect.ComputerServiceClient

// Dial builds a bearer-authenticated WorkerService client (h1).
func Dial(ep Endpoint) WorkerClient {
	hc := &http.Client{}
	var ics []connect.ClientInterceptor
	if ep.Token != "" {
		ics = append(ics, bearerInterceptor(ep.Token))
	}
	return workerv1connect.NewWorkerServiceClient(connect.NewClient(
		connecthttp.NewTransport(hc, normalizeURL(ep.URL), connecthttp.WithReadMaxBytes(0)),
		ics...,
	))
}

// DialComputer builds a bearer-authenticated ComputerService client (h1).
func DialComputer(ep Endpoint) ComputerClient {
	hc := &http.Client{}
	var ics []connect.ClientInterceptor
	if ep.Token != "" {
		ics = append(ics, bearerInterceptor(ep.Token))
	}
	return workerv1connect.NewComputerServiceClient(connect.NewClient(
		connecthttp.NewTransport(hc, normalizeURL(ep.URL), connecthttp.WithReadMaxBytes(0)),
		ics...,
	))
}

// ClientCache caches clients per (url, token), LRU-capped.
type ClientCache struct {
	mu      sync.Mutex
	max     int
	workers map[string]WorkerClient
	comps   map[string]ComputerClient
}

// NewClientCache builds a cache capped at max entries (default 16).
func NewClientCache(max int) *ClientCache {
	if max <= 0 {
		max = 16
	}
	return &ClientCache{max: max, workers: map[string]WorkerClient{}, comps: map[string]ComputerClient{}}
}

func key(ep Endpoint) string { return normalizeURL(ep.URL) + "\x00" + ep.Token }

// Worker returns a cached WorkerService client for ep.
func (c *ClientCache) Worker(ep Endpoint) WorkerClient {
	c.mu.Lock()
	defer c.mu.Unlock()
	k := key(ep)
	if hit, ok := c.workers[k]; ok {
		return hit
	}
	cl := Dial(ep)
	c.workers[k] = cl
	if len(c.workers) > c.max {
		for oldest := range c.workers {
			if oldest != k {
				delete(c.workers, oldest)
				break
			}
		}
	}
	return cl
}

// Computer returns a cached ComputerService client for ep.
func (c *ClientCache) Computer(ep Endpoint) ComputerClient {
	c.mu.Lock()
	defer c.mu.Unlock()
	k := key(ep)
	if hit, ok := c.comps[k]; ok {
		return hit
	}
	cl := DialComputer(ep)
	c.comps[k] = cl
	if len(c.comps) > c.max {
		for oldest := range c.comps {
			if oldest != k {
				delete(c.comps, oldest)
				break
			}
		}
	}
	return cl
}
