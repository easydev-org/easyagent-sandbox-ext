# AGENTS.md — easyagent-sandbox-ext

Working notes for agents editing this repo.

This extension drives **easyworker** sandboxes: shell, files, jobs, and (where the accessibility CLI
is present) native GUI automation. It carries no cluster credentials and no database of its own —
every operation is an RPC to a worker that already exists.

**End-to-end testing follows the shared convention in
[`easybus-sdk/E2E.md`](https://github.com/easydev-org/easybus-sdk/blob/main/E2E.md).** Read that
first; this file only records what is specific to this repo.

---

## The two test suites

| job | services | proves |
|---|---|---|
| `unit` | none | build, vet, `-race` tests |
| `live` | nats + **easyworker (labwc)** | a real file written through the extension is read back **from the sandbox** |

## What the live suite needs, and why

```yaml
# .github/ci/compose.yaml
sandbox:
  image: ghcr.io/easydev-org/sandbox-wayland:main   # easysandbox's labwc flavour
  ports: ["48080:48080"]        # published: the extension runs on the RUNNER
  shm_size: 2gb                 # a compositor dies in the default 64MB
  environment:
    WORKER_TOKEN: "ci-token"    # without it the worker boots UNCLAIMED
```

* **`sandbox-wayland:main`, not a fake.** The extension's whole job is talking to an easyworker;
  a stub would test the stub. The image is published by `easysandbox`'s own release workflow, so it
  is the same image a deployment runs.
* **`main`, not `latest`.** `latest` can lag, which would let this suite pass against an image that
  no longer ships.
* **One long-lived container, never created per run.** These tools act on a sandbox that already
  exists; only `sandbox-job-*` would start one. That is why the suite needs **no Docker-in-Docker**.

## Traps in this repo specifically

### The worker must be claimed

Without `WORKER_TOKEN` at boot the log says:

```
easyworker UNCLAIMED — enrollment code: d586065918…
```

and every call fails. The token in the worker's environment and the token in the extension's
`sandboxes` config **must match**; a mismatch is a 401 the extension reports as a transport failure
naming nothing.

### `sandboxes` is `required_config` on every tool

Except `list-sandboxes`, which correctly answers *"none configured"*. Do not assert a refusal there
— an early version of that test did, and failed against correct behaviour.

### Health check: no `wget`, no `curl`

The probe goes through `bash -c` and `/dev/tcp` (bash, because Debian's `/bin/sh` is dash). The
first attempt used `wget` and spent 40 probes reporting `/bin/sh: 1: wget: not found` while the log
plainly showed `easyworker listening on 0.0.0.0:48080`.

### Registration is asynchronous

The suite waits for `Discover()` to return this extension before the first tool call; without that
the first call gets `no responders`, which is indistinguishable from "no such tool".
