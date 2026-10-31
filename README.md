# easyagent-sandbox-ext

A standalone easyagent **extension server** that drives a config-registered set
of remote [**easyworker**](https://github.com/easydev-org/easyworker) sandboxes
over the easybus protocol. It is the **Go rebrand of the worker-extension**.

Against the same set of sandboxes it:

- runs commands and reads / writes files (`sandbox-*` tools), and
- drives a **native GUI** through the platform accessibility tree
  (`computer-*` tools) via the worker's `ComputerService`.

## Sandboxes

One `sandboxes` config (a JSON list of `{name,url,token}`); every tool takes an
optional `sandbox` argument selecting one by name (the first is the default).

```json
[{"name": "pod-a", "url": "http://worker:80", "token": "..."}]
```

`token` is a **secret**: the agent withholds its value from clients.

## Tools

**Jobs** `sandbox-info`, `sandbox-job-start` (with a REQUIRED `lang`),
`sandbox-job-output`, `sandbox-job-wait`, `sandbox-job-kill`, `sandbox-job-list`,
`sandbox-job-stdin`

**Files** `sandbox-file-read`, `sandbox-file-list`, `sandbox-file-patch`,
`sandbox-download`, `sandbox-upload`

`sandbox-file-patch` is the ONLY tool that writes: one `*** Begin Patch` document
creates, edits and deletes, and nothing is written until every hunk has been proven
to apply. It replaces the separate write/delete/move/copy tools, which did the same
thing four ways — and a patch says what the change IS, where resending a whole file
does not.

`lang` has no default on purpose: `echo hi && ls` is bash, is a syntax error in
Windows PowerShell 5.1, and means something else in PowerShell 7. Read
`sandbox-info` for the languages a sandbox can actually run (the engine lists only
interpreters it PROBED by running one), and prefer `python` when it is there.

**GUI** `computer-info`, `computer-apps`, `computer-snapshot`, `computer-find`,
`computer-action`, `computer-click`, `computer-type`, `computer-key`,
`computer-scroll`, `computer-drag`, `computer-screenshot`

**Discovery** `list-sandboxes`

## Wire contract

The extension speaks **worker.v1** (Connect RPC) to each sandbox. The proto is
**vendored** under `proto/worker/v1/` (copied from easyworker) and the Go
client/server stubs are generated with `buf` (connect-go v2).

## Build

```bash
buf generate --template proto/buf.gen.yaml --path proto   # refresh gen/
go build ./cmd/sandbox-ext
./build-image.sh
```

## Test

```bash
go test ./...
```

Tests use an in-process fake worker (no real sandbox needed).
