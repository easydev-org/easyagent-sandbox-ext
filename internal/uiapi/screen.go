package uiapi

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

// The browser's side of the screen stream: same-origin, so the panel can open it directly.
var browserUpgrader = websocket.Upgrader{
	// Same-origin by construction — the host proxies /ext/sandbox — but the check is left permissive
	// here because the AUTH is the bearer below, not the origin. A cross-origin caller with a valid
	// token is still a valid caller.
	CheckOrigin: func(*http.Request) bool { return true },
	// Frames are H.264 access units, a few KB to tens of KB each.
	ReadBufferSize:  32 * 1024,
	WriteBufferSize: 32 * 1024,
}

// screen forwards the sandbox's H.264-over-WebSocket stream to the browser.
//
// WHY PROXY AT ALL: the worker serves `/est/ws` on its own ClusterIP, which a browser cannot reach, and
// that endpoint has no authentication of its own — it is guarded by being inside the pod network. So
// this is where the bearer is checked: the panel may only watch a sandbox it owns.
//
// WHY WEBSOCKET AND NOT MJPEG OR SSE: the worker already produces H.264 access units and the browser
// already has a decoder for them (WebCodecs `VideoDecoder`). Re-encoding here would cost CPU per viewer
// and lose the keyframe/parameter-set structure the decoder needs.
func (h *handler) screen(w http.ResponseWriter, r *http.Request, name string) {
	sb, ok := h.findSandbox(w, r, name)
	if !ok {
		return
	}

	upstreamURL, err := estURL(sb.URL)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	header := http.Header{}
	if sb.Token != "" {
		// The worker authenticates the CONNECT RPCs by bearer; the est endpoint rides the same
		// listener behind the same interceptor, so the token goes along as well.
		header.Set("Authorization", "Bearer "+sb.Token)
	}
	// The WORKER'S ORIGIN, not the browser's.
	//
	// `golang.org/x/net/websocket` — which the worker uses — rejects a handshake with no `Origin`
	// header with a bare 403. A browser always sends one, but this proxy is the client here, so it
	// has to send one too. Measured: without this every screen request failed with 502, because the
	// worker refused the upgrade and the dial error surfaced as a bad gateway.
	if u, perr := url.Parse(upstreamURL); perr == nil {
		header.Set("Origin", "http://"+u.Host)
	}

	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
		// Frames are small and frequent; the default 4KB buffer would force a copy per frame.
		ReadBufferSize:  32 * 1024,
		WriteBufferSize: 32 * 1024,
	}
	up, _, err := dialer.DialContext(r.Context(), upstreamURL, header)
	if err != nil {
		// 502 with the reason: "the sandbox is not answering" is the useful fact here, and a bare
		// status would leave the panel guessing whether the problem was its own.
		writeErr(w, http.StatusBadGateway, "screen stream: "+err.Error())
		return
	}
	defer func() { _ = up.Close() }()

	down, err := browserUpgrader.Upgrade(w, r, nil)
	if err != nil {
		// Upgrade already wrote a response.
		return
	}
	defer func() { _ = down.Close() }()

	// Two pumps. Each closes its OWN side when it finishes, which is what unblocks the other: a
	// reader waiting on a socket that will never speak again would otherwise leak this goroutine for
	// the life of the process.
	errc := make(chan error, 2)
	go pump(up, down, errc)
	go pump(down, up, errc)
	<-errc
}

// pump copies messages one way and reports why it stopped.
func pump(dst, src *websocket.Conn, errc chan<- error) {
	for {
		typ, data, err := src.ReadMessage()
		if err != nil {
			_ = dst.Close()
			errc <- err
			return
		}
		if err := dst.WriteMessage(typ, data); err != nil {
			_ = src.Close()
			errc <- err
			return
		}
	}
}

// estURL turns a worker base URL into its `/est/ws` WebSocket URL.
//
// The scheme is rewritten rather than concatenated: a worker URL is `http://worker:8080`, and a
// WebSocket handshake needs `ws://`. Getting this wrong produces a plain GET that the worker answers
// as a 404, which reads as "no screen" rather than "wrong scheme".
func estURL(base string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil {
		return "", err
	}
	switch u.Scheme {
	case "http", "":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	case "ws", "wss":
		// Already one.
	default:
		return "", errors.New("unsupported sandbox URL scheme: " + u.Scheme)
	}
	u.Path = strings.TrimSuffix(u.Path, "/") + "/est/ws"
	u.RawQuery = ""
	return u.String(), nil
}

// contextWithCancel gives a handler a context that ends when the request does.
//
// A local wrapper rather than `r.Context()` inline, so every call site got the cancel func and none
// of them leaked the derived context.
func contextWithCancel(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithCancel(r.Context())
}
