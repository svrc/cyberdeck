# cyberdeck

*V: "Hm... whadda we got here? Looks like... a box."*

*Johnny: "You're a box. Try cyberdeck."*

Cyberdeck is an orchestrator for building & maintaining nested VMware Cloud Foundation 9.x+ labs.  

It targets vSphere and KVM through one `Hypervisor` interface, drives deployments via Temporal workflows. It's a fully open source complementary alternative to the HoloDeck commmunity supported by Broadcom.   

We built this primarily so we can run VCF labs on a public cloud for minimal cost: bare metal instances with ephemeral high-speed NVMe drives, no need for expensive EBS or IOPS reservations or extensive scalable external storage.  Just S3 object storage to keep your compressed state, with a goal SLO to snapshot or restore your multi-TB VSAN drives within 20 minutes.   Minimal cost is not zero cost, a `z1d.metal` VM for example , which can host a single site full VCF deployment, is still $4.45/hr USD.   S3 costs for a full site are still $30+ a month.   For you, this may be worth it over investing $10k+ in a hardware lab.  

**Status: iniital spike complete.** Hypervisor interface + 3 backends (mock, vSphere via govmomi, KVM via pure-Go libvirt RPC), CreateNestedESXi Temporal workflow, both in-process testsuite and real-Temporal worker modes — all 8 (runtime × backend) combinations validated against either simulator or live infra.

## Documentation

- **[docs/architecture.md](docs/architecture.md)** — component diagram + design rationale
- **[docs/testing.md](docs/testing.md)** — three test layers (functional / local-integration / cloud) + runbooks
- **[docs/roadmap.md](docs/roadmap.md)** — phased plan + live TODO

## Layout

```
cmd/cyberdeck/             cobra CLI: `spike`, `server`
internal/spec/             VMSpec / DiskSpec / NICSpec — hypervisor-agnostic shapes
pkg/hypervisor/            interface + impls
  mock/                    in-memory, used by workflow tests
  conformance/             reusable test suite any impl runs against
  vsphere/                 govmomi-backed (vCenter + ESXi-direct + simulator)
  kvm/                     pure-Go libvirt RPC (digitalocean/go-libvirt)
pkg/workflow/              Temporal workflow + activities + worker + run helpers
config/                    legacy holorouter templates (FRR, dnsmasq, k8s,
                           VCF installer JSON specs) — reference for porting
docs/                      architecture / testing / roadmap
```

## Quick start

```sh
# Layer A — fully in-process, no infra
go test ./...

# Layer B — local integration (see docs/testing.md for setup)
brew install temporal
temporal server start-dev --headless &
TEMPORAL_ADDR=localhost:7233 go test -run RunOnce ./pkg/workflow/...
```

## Spike CLI

```
# In-process Temporal testsuite (no server needed)
cyberdeck spike --backend mock
cyberdeck spike --backend vsphere
cyberdeck spike --backend kvm                       # needs LIBVIRT_URI

# Real Temporal server (run `temporal server start-dev` first)
cyberdeck spike --backend vsphere --temporal localhost:7233
```

Default runs the workflow via `testsuite.WorkflowTestSuite` — fully in-process, no infra. With `--temporal addr` the same workflow runs against a real Temporal server: cyberdeck spawns an in-process worker, submits the workflow, waits for the result, stops the worker.

## Long-running worker (production shape)

```
temporal server start-dev &
cyberdeck server --backend vsphere --temporal localhost:7233
```

Registers `CreateNestedESXi` workflow + activities and blocks on `cyberdeck-spike` task queue until SIGINT. Workflows can be submitted from anywhere — the `temporal` CLI works:

```
temporal workflow start --address localhost:7233 \
  --task-queue cyberdeck-spike --type CreateNestedESXi \
  --workflow-id my-wf-1 \
  --input '{"Spec":{"Name":"my-wf-1","VCPUs":4,...},"ExtraDisks":[],"ExtraNICs":[]}'

temporal workflow result --address localhost:7233 --workflow-id my-wf-1
```

**gotcha**: don't use `go run ./cmd/cyberdeck server …` for testing — `go run` doesn't propagate signals to the compiled binary, so killing it leaves an orphan worker polling the task queue. Build first: `go build -o /tmp/cyberdeck ./cmd/cyberdeck && /tmp/cyberdeck server …`.

## Why these dependencies

- `vmware/govmomi` (Apache-2.0) — official vSphere SDK; replaces PowerCLI.
- `digitalocean/go-libvirt` (Apache-2.0) — pure-Go libvirt RPC client. No cgo, easy cross-compile.
- `go.temporal.io/sdk` (MIT) — Temporal SDK.
- `spf13/cobra` (Apache-2.0) — CLI.
- `stretchr/testify` (MIT) — assertions.

