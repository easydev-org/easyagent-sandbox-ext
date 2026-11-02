// Package uiapi is the browser panel's API.
//
// THE EXTENSION SPEAKS THE BUS AND CALLS EACH WORKER'S CONNECT API, which is all a TOOL needs. A panel
// needs more than that: a same-origin URL a browser can open, and the browser cannot reach a worker's
// ClusterIP. So this package is the door.
//
// THREE FILES, AND THE SPLIT IS THE DESIGN:
//
//	connect.go   the API — ListSandboxes, ListJobs, GetJobOutput, WatchJob (streamed), AuthorizeDesktop
//	desktop.go   the screen — a proxy to the worker's own easystream UI, and the signatures that gate it
//	identity.go  a bearer, resolved to the tenant it belongs to
//
// THE SCREEN IS NOT AN RPC, AND THAT IS CORRECT. It is a WebSocket carrying video one way and input the
// other; easystream-webui already paints it, with zoom, a virtual keyboard and touch, so the work here
// is to forward rather than to reimplement. See desktop.go for why the paths look the way they do.
//
// THE API WAS ONCE HAND-WRITTEN JSON ROUTES. Their shapes were `map[string]any` literals written at the
// point of emission, so the panel's contract was a side effect of whatever a handler happened to emit —
// the arrangement that has already produced an empty file tree in another extension when a route wrote
// `Path` and the panel read `path`. They are Connect RPCs now, generated from a .proto the panel shares.
package uiapi
