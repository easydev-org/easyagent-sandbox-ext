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

**Jobs** `sandbox-info`, `sandbox-exec`, `sandbox-job-start`,
`sandbox-job-output`, `sandbox-job-wait`, `sandbox-job-kill`, `sandbox-job-list`

**Files** `sandbox-file-read`, `sandbox-file-write`, `sandbox-file-list`,
`sandbox-file-delete`, `sandbox-file-move`, `sandbox-file-copy`,
`sandbox-download`, `sandbox-upload`

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
