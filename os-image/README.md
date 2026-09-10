# Reference node OS image

Ubuntu 26.04 image that ships `nke-agent` and `spire-agent` so a site
controller can reach the node over SPIFFE mTLS, built on Ubuntu's
NVIDIA-tuned kernel flavor (`linux-nvidia`/`linux-nvidia-64k`), not the
generic default. The contract is [SPEC.md](SPEC.md).

| Path | What |
|---|---|
| `SPEC.md` | Image contract (bake vs first-boot, ports, identity) |
| `packer/` | Packer + QEMU recipe that produces a qcow2 |
| `files/` | systemd units, SPIRE template, first-boot script |
| `files/luks/` | LUKS/TPM2 first-boot binding assets for the FDE variant |
| `files/lvm/` | First-boot storage-growth assets for the non-FDE (no encryption) variant |
| `docs/full-disk-encryption.md` | FDE variant: build, verify, failure modes |
| `demo-fde.sh` | Runs the host-FDE acceptance scenarios against a built image |
| `cmd/nke-os-image-check` | Connectivity checker (Go) |

## Build the OS image

Packer first tries to build packages in a native Linux container through a
`shell-local` provisioner. If the container build is unavailable or fails,
the node-image VM builds from the same staged sources and locks. Packer/QEMU
uses HVF on Apple Silicon or KVM on dedicated ARM64 and AMD64 Linux hosts.
The container never needs privileged mode or a Docker socket. The checkout is
mounted read-only; downloads use a Docker volume, and successful output is
exported under `packer/artifacts/<arch>/debs/`.

| Host | Packages | Bake and boot tests |
|---|---|---|
| Apple Silicon Mac | Native Linux ARM64 container | QEMU ARM64 with HVF |
| ARM64 Linux | Native Linux ARM64 container | QEMU ARM64 with KVM |
| AMD64 Linux | Native Linux AMD64 container | QEMU AMD64 with KVM |

On each host, the image VM is the fallback package builder when the container
attempt does not produce debs.

Required host tools: Go per `go.work`,
Packer, QEMU, UEFI firmware, `cloud-localds`, `swtpm`, `xorriso`, `7z`, GNU
coreutils (`sha256sum`, `timeout`), curl and SSH. QEMU's bundled EDK2 firmware
is discovered automatically on Nix; Ubuntu's OVMF/AAVMF paths also work.
On a Nix-managed host, declare these tools in your environment; for a one-off
session with missing image tools:

```bash
nix shell nixpkgs#swtpm nixpkgs#cloud-utils nixpkgs#xorriso nixpkgs#p7zip nixpkgs#coreutils
```

From the repository root:

```bash
# Just build the node packages; ARCH defaults to the host architecture.
make os-image-packages ARCH=arm64

# Dedicated native Linux host: build packages, then the production node image.
make os-image-native ARCH=arm64 VARIANT=non-fde
make os-image-native ARCH=amd64 VARIANT=fde

# Apple Silicon development image; choose the generic guest kernel explicitly.
make os-image-native ARCH=arm64 VARIANT=non-fde KERNEL_PACKAGE=linux-generic

# Boot checks against the resulting image (FDE has its own test).
make -C os-image boot-smoke ARCH=arm64 REQUIRE_ACCELERATION=1
./os-image/demo-fde.sh  # existing amd64 FDE harness
```

The existing Packer Make targets and direct `packer build` invocations use
the same `shell-local` package attempt. The root Make targets delegate to
Packer; they do not run a second package build. Docker is optional for image
bakes, but required for the standalone container package target.

`os-image-native` requires working native hardware acceleration. An unavailable
accelerator fails before the bake. A missing, unreachable, or wrong-architecture
container engine falls back to the guest package build.
`os-image-fde` and `os-image-non-fde` use the same flow with optional TCG fallback
for development; `ACCELERATOR=tcg` selects emulation explicitly. Container
package builds always require a native engine. `CONTAINER_ENGINE` selects a
Docker-compatible CLI; `BUILDER_IMAGE` overrides the locally built image name.
The container daemon must be able to bind-mount the checkout and output
paths (on macOS, keep them in directories shared with the runtime).

`make -C os-image packages` builds the pinned builder image and uses the existing
package recipes. `K8S_VERSIONS` and `CALICO_VERSIONS` select versions with committed
locks. OVN is opt-in with `BUILD_OVN=1` and requires its restricted inputs and a
completed lock; it is not part of the public default build. Standalone package
build failure preserves the previous successful output. The Packer host
wrapper preserves the entire previous upload directory outside the next upload
and stages only allowlisted build sources, so a failed attempt cannot reuse
stale packages or upload local caches and unrelated files. Replaced successful
outputs are retained under the host builder cache, outside the directory
Packer uploads. Downloaded and staged package content
lives in the Docker volume `nke-package-builder-<arch>` (override with
`BUILDER_CACHE_VOLUME`), so extraction uses Linux filesystem semantics even
on a Mac. `BUILDER_CACHE` selects the host directory for retained outputs.

`make -C os-image packages-lock ARCH=arm64` resolves new upstream checksums and
image digests inside the builder and exports them to `packer/artifacts/arm64/lock`.
Review and copy the resulting per-package architecture directories into each
recipe's `lock/` directory. Lock generation never edits the checkout itself.

The bake verifies the exported checksums and architecture, builds `nke-agent`
from this checkout, and downloads pinned SPIRE and TPM-plugin binaries for the
**guest** architecture. It stages the debs as a local apt repository; package
build tools normally stay in the container. If debs are absent, the guest
uses the shared package script and staged build choices, then removes its
temporary toolchain. Guest uploads and build scratch space use disk-backed
`/var/tmp/nke-image`, avoiding Ubuntu’s memory-backed `/tmp` during large
archive repacks. The entire staging tree is removed before export. Existing debs with failed integrity checks abort the bake.
Both FDE and non-FDE use this flow.

The default kernel remains `linux-nvidia` on AMD64 and `linux-nvidia-64k` on
ARM64. `KERNEL_PACKAGE=linux-generic` is an explicit development override, not
production-kernel validation. Verify production images on the native Linux
hosts. Build-time identity and SSH keys are cleared before export; see SPEC.md.

## Full-disk-encryption (FDE) variant

An opt-in build that lays down an LVM-on-LUKS2 root volume via the Ubuntu
Server installer + autoinstall, then defers TPM 2.0 binding to first boot on
the target hardware. See [docs/full-disk-encryption.md](docs/full-disk-encryption.md)
for the full design, verification steps, and (unrecoverable-by-design)
failure modes.

```bash
cd packer
make os-image-fde
```

Output: `packer/output-fde/nke-node-fde-ubuntu-<version>-<arch>.qcow2` (e.g.
`nke-node-fde-ubuntu-26.04-amd64.qcow2`). `LUKS_PASSPHRASE` (default
`changeit` — **set this to a real value for any build that will run on real
hardware**; see [docs/full-disk-encryption.md](docs/full-disk-encryption.md))
sets the build-time-only passphrase, which the shipped image never retains
once a node's first boot completes a verified TPM bind.

## Non-FDE variant (no encryption)

The same installer + autoinstall + LVM flow as the FDE variant, with
encryption turned off entirely — no LUKS, no Clevis, no TPM binding. Useful
for telling FDE-specific issues apart from issues in the installer flow
itself (partitioning, identity/SSH setup, first-boot storage growth), or as
a starting point if you want the installer-based LVM layout without opting
into encryption.

```bash
cd packer
make os-image-non-fde
```

Output: `packer/output-non-fde/nke-node-non-fde-ubuntu-<version>-<arch>.qcow2`
(e.g. `nke-node-non-fde-ubuntu-26.04-amd64.qcow2`). Boot it with
`boot-smoke.sh` (it needs `OVMF_CODE` set, since this variant boots UEFI —
see the comment at the top of that script; `QCOW` defaults to the single
qcow2 under `output-non-fde/`, so it usually doesn't need to be passed):

```bash
OVMF_CODE=/usr/share/OVMF/OVMF_CODE_4M.fd ./scripts/boot-smoke.sh
```

## Interactive VM

`make run-vm QCOW=<path>` boots any qcow2 this repo produces and leaves it
running with SSH forwarded, for poking around manually — unlike
`boot-smoke`/`demo-fde.sh`, it runs no checks and doesn't tear itself down.
It prints the exact `ssh` command to use once the VM is up (key path and
port included); by default that's:

```bash
ssh -i packer/.ssh/id_ed25519 -p 2222 -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null ubuntu@127.0.0.1
```

**To quit, use `Ctrl-A` then `X`, not plain `Ctrl-C`**: QEMU runs with
`-nographic`, so your terminal is the guest's serial console, and plain
`Ctrl-C` is sent to the guest as literal input (garbling a login prompt)
instead of stopping QEMU. See the comment at the top of
[`packer/scripts/run-vm.sh`](packer/scripts/run-vm.sh) for the full env var
list (`OVMF_CODE` defaults automatically per `ARCH` since every qcow2 this
repo builds is UEFI-only; `TPM=1` attaches a TPM for the FDE image).
Examples:

```bash
make run-vm QCOW=packer/output-non-fde/nke-node-non-fde-ubuntu-26.04-amd64.qcow2
make run-vm QCOW=packer/output-fde/nke-node-fde-ubuntu-26.04-amd64.qcow2 TPM=1
make run-vm QCOW=packer/output-non-fde/nke-node-non-fde-ubuntu-26.04-arm64.qcow2 ARCH=arm64
```

## Release

An `os-image/vX.Y.Z` tag (or `os-image/vX.Y.Z-rc.N`) runs
`.github/workflows/os-image-artifact.yml`, which builds and validates the
amd64 FDE and non-FDE images and the arm64 non-FDE image and, after each
architecture passes its checks,
pushes the qcow2s to GHCR as OCI artifacts under arch-suffixed tags — not
one multi-platform manifest shared across arches, see the workflow's header
comment for why — and cuts a GitHub Release for the amd64 build attaching
`nke-os-image-check` and `checksums.txt` (covering the amd64 qcow2s and the
checker binary; the arm64 qcow2 isn't attached to the release yet, only
GHCR).

```bash
oras pull ghcr.io/nvidia/nke/os-image-fde:vX.Y.Z-amd64 --platform linux/amd64
oras pull ghcr.io/nvidia/nke/os-image-non-fde:vX.Y.Z-amd64 --platform linux/amd64
oras pull ghcr.io/nvidia/nke/os-image-non-fde:vX.Y.Z-arm64 --platform linux/arm64
```

**FDE security note**: the published image's `LUKS_PASSPHRASE` is the
shared, documented build-time placeholder (`changeit`), baked into both a
LUKS passphrase slot and GRUB's `luks.key=` kernel parameter until a node's
first real boot completes a verified TPM bind (see
[docs/full-disk-encryption.md](docs/full-disk-encryption.md)). Anyone with
console/physical access to an unbound node can read it and unlock the
volume with it — this is a known, accepted tradeoff for publishing a
build-and-go FDE image without a per-node secret-provisioning story.
Verify a node closed that window with `luks-tpm-verify` before trusting
TPM-only unlock in production.

The workflow selects dedicated Linux runners with `OS_IMAGE_AMD64_RUNNER`
and `OS_IMAGE_ARM64_RUNNER` repository variables (see the workflow README for
fallback labels). Hosts must provide readable,
writable `/dev/kvm`. Preflight fails on a missing device or architecture
mismatch; CI does not fall back to TCG or change host device permissions.

If the FDE/non-FDE build or boot-smoke steps fail in CI, download the
`fde-install-console-logs` artifact from the run — it's the guest's own
serial console output (boot + autoinstall), uploaded regardless of outcome
since there's no VNC/monitor access on the runner to inspect a stuck build
interactively.

The baked `nke-agent` is built from the tagged commit's `agent/` source, so
the agent version is fixed by the ref the workflow runs on.

## First boot (cloud-init)

Do **not** bake SPIRE server, trust domain, bundle, or join token. Pass them
as instance user-data, for example:

```yaml
#cloud-config
write_files:
  - path: /etc/nke/spire.env
    permissions: "0600"
    content: |
      SPIRE_SERVER_ADDRESS=spire.example.com
      SPIRE_SERVER_PORT=8081
      SPIRE_TRUST_DOMAIN=example.org
      SPIRE_JOIN_TOKEN=REDACTED
  - path: /etc/nke/trust-bundle.pem
    permissions: "0644"
    content: |
      -----BEGIN CERTIFICATE-----
      ...
      -----END CERTIFICATE-----
```

`nke-spire-configure.service` waits for `cloud-final`, renders
`/etc/spire-agent/agent.conf`, and only then does `spire-agent` start.
`nke-agent` starts after the workload API socket exists.

The site SPIRE server must also register a workload entry for root
`nke-agent` (unix attestor, uid 0) and a `/nke-service/...` identity for
whatever will dial the node (controller or `nke-os-image-check`).

## Checker

From the repository root (`make build` writes `bin/nke-os-image-check`):

```bash
nke-os-image-check --endpoint 192.0.2.10:15005
```

The process needs a SPIFFE workload API with an SVID whose path starts with
`/nke-service/`, in the same trust domain as the node. Default socket:

`unix:///tmp/spire-agent/public/api.sock`

Override with `--spiffe-socket`. This is the same shape as the controller
dialer; it is not a SPIRE server. For a local SPIRE, see
`controller/scripts/dev-env-spire.sh`.
