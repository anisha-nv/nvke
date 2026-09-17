# Package-builder reference

For installation and the usual build command, start with the [quick start](README.md).

## Launch a VM manually

From the repository root, on Apple Silicon macOS or amd64/arm64 Linux:

```bash
nix build ./os-image#builder-arm64 -o result-builder-arm64
nix build ./os-image#builder-amd64 -o result-builder-amd64
```

For Git-flake builds, the flake files must be tracked by Git. Ignored Packer keys
and artifacts are excluded from that source snapshot.

These outputs are launchers plus their guest closures, not golden qcow2 images.
Nix builds the Linux system derivations using a matching Linux builder or binary
cache. No derivation uses `make-disk-image` or boots an image-assembly VM.
At launch, host tools create the writable disk and an EROFS store filesystem;
QEMU boots the supplied kernel and initrd directly, without UEFI or an installer.

Supply a public key at runtime; no credentials enter the Nix store:

```bash
NKE_BUILDER_SSH_KEY="$HOME/.ssh/id_ed25519.pub" SSH_PORT=2222 \
  /absolute/path/to/result-builder-arm64/bin/nke-builder-arm64
```

Connect as `root` with the corresponding private key. The launcher forwards SSH
only on `127.0.0.1`, and password authentication is disabled. It prefers HVF on
matching Apple Silicon hosts or usable KVM on matching Linux hosts, otherwise
TCG. Set `ACCELERATOR=tcg` to force emulation. Explicit `kvm` or `hvf` requests
are passed to QEMU, which rejects unsupported combinations. `NIX_DISK_IMAGE`
overrides the writable disk location; `SSH_PORT` defaults to 2222.

## Persistent cache

Each architecture reuses a sparse, 32 GiB writable disk at
`${XDG_CACHE_HOME:-$HOME/.cache}/nke/builders/<arch>/root.qcow2`. Set
`NKE_BUILDER_STATE_DIR` to choose a different state directory. The writable
`/nix/store` overlay and `/nix/var` database live on this disk, along with the
default Go module, Go build, and Zig compiler caches under `/root`. They survive shutdown and
restart; the writable store is not a tmpfs. The 32 GiB capacity is shared by
all writable guest data, and the host file grows as blocks are used.

The base system's read-only store filesystem is prepared by the host at launch.
Managed builds reuse `runtime/` in the state directory for this derived image;
they do not leave a separate host store image for every run.
Shut the guest down cleanly (`ssh ... poweroff`) before reusing its disk. Run
only one VM per state directory at a time. No automatic cache deletion or
garbage collection is configured.

The VM has 4 CPUs, 4 GiB RAM, a 32 GiB writable disk, Go 1.26, a C compiler,
Debian packaging utilities, and the download/archive tools used by the existing
package scripts. `nix-ld` supports downloaded executables using conventional
Linux dynamic-loader paths. The guest does not include Packer or a QEMU system
emulator. The input follows `nixos-unstable`; `flake.lock` pins the exact revision until an
explicit `nix flake update nixpkgs --flake ./os-image` update.

## Nix app: build packages

Nix is the entrypoint and defines the shell applications, host tools, and VM launcher:

```bash
K8S_VERSIONS=v1.36.3 nix run ./os-image#build-packages-arm64 -- \
  --output /absolute/path/to/artifacts
```

Use `build-packages-amd64` for x86 packages. `--source /path/to/checkout` selects
a different source checkout; by default the app finds the Git root of the current
directory. Flake files must be tracked by Git (including intent-to-add for new
files). Use the Git-backed `./os-image` reference, not `path:./os-image`, so ignored
Packer SSH keys, ISOs, and artifacts do not enter the flake source.

Nix resolves the VM and pins Bash, coreutils, flock, Git, OpenSSH, and rsync.
The flake-defined shell application starts the VM, waits for SSH, transfers source,
runs the guest build script, retrieves artifacts, and shuts down through an exit
trap. A small shell application shares the SSH options with rsync. The guest
script runs the existing package targets and ELF check. There is no Python
coordinator or recursive Nix invocation. The Ubuntu Packer VM remains a separate host process.
Neither guest runs QEMU.

Packer's `fetch-artifacts.sh` uses a small shell adapter:
`NKE_PACKAGE_BUILDER=auto` keeps direct builds on matching Linux hosts and calls
the Nix app otherwise. Set `host` or `nix` to choose explicitly. The normal
`make -C os-image image ARCH=arm64` entrypoint uses this same path. A VM package
build requires Nix on the host; its other host tools come from the flake.
Building the guest closure still needs a matching Linux Nix builder or available
substitutes. QEMU emulation at runtime does not replace that build step.

Tracked files (including local modifications and vendor/) plus non-ignored
untracked files are transferred at runtime. Ignored host outputs and `.git`
are excluded. The host computes the agent version with `git describe` and
passes it with the guest source root to Make, so linked worktrees do not need
their external Git metadata copied. Keep local credentials ignored by Git.
Supported package version overrides and optional `NGC_API_KEY` travel through
SSH stdin, not process arguments or source archives.

The guest uses `pkgs.nfpm` from the locked nixpkgs revision. Native builds retain
the tool version in `make/tool-versions.mk`; the VM supplies its version through
`NFPM_BIN`, so package rules do not download a second copy.
Existing package rules fetch checksum-verified crane releases. Package tool and
download directories live in `/root/.cache/nke-packages/`; their existing scripts
still decide what can be reused. Go and Zig caches persist too. Source snapshots
and guest outputs remain under `/root/nke-builds/<run-id>` for inspection; monitor
the shared 32 GiB disk when running repeated builds.

`NKE_BUILDER_STATE_DIR` selects the persistent disk/key/log directory; one managed
build at a time holds its lock. The guest refreshes its authorized public key on
each boot, including when reusing a disk. Do not launch a manual VM on that disk
concurrently. Managed apps default to SSH port 2222 for arm64 and 2223 for amd64;
set `SSH_PORT` when running another instance with separate state.
`NKE_BUILDER_BOOT_TIMEOUT` defaults to 900 seconds, including host
store preparation. Use `NKE_BUILDER_ACCELERATOR=tcg` to force software emulation
independently of Packer's `ACCELERATOR`. A failed package build shuts down the VM
and does not collect partial outputs; logs and guest files remain available.

### Ubuntu-compatible linking

The guest sets `CC` to `nke-ubuntu-cc`, a wrapper around the pinned nixpkgs
`zig_0_15` C compiler. It targets the guest architecture with the glibc 2.39 ABI
(Ubuntu 24.04's baseline), using Zig's bundled libc headers and link stubs.
The output uses Ubuntu's standard dynamic loader and system libc, with no Nix
store RPATH. This also targets the newer Ubuntu 26.04 used by the Packer recipe.
The Zig dependency supplies the target ABI independently of the NixOS host's
libc; merely changing a Nix-linked binary's interpreter would not do that.

The existing agent Makefile honors `CC`, so native `make -C agent deb-arm64`
(or `deb-amd64`) uses this compiler without changing the agent's build rules.
For packaging, supply the repository-pinned nfpm through `NFPM_BIN` when it is
already provisioned. Keep cgo enabled for the agent's dynamic libc dependency. The updater currently
uses no cgo and remains static. A compiler/sysroot for additional external C
libraries is outside this wrapper's scope; those need explicit target libraries.

`check-ubuntu-elf --arch arm64 PATH_TO_AGENT` rejects the wrong loader,
runtime library search paths, path-qualified library dependencies, and glibc
requirements newer than 2.39. It is for dynamically linked executables, not
the static updater. The builder installs this check as a Nix-defined shell application with pinned
binutils, grep, and sed. The shell smoke app runs it on its CGo probe.

For a runtime check, unpack a checksum-verified Ubuntu ARM root filesystem into
a **disposable directory** on the Linux builder, then run:

```bash
bash os-image/nix/test-ubuntu-package.sh /path/to/nke-agent.deb /path/to/ubuntu-root
```

This extracts the package and executes the packaged agent and updater with `--version`
inside `chroot`, with no `/nix` and a clean environment. It needs root and a
matching CPU architecture. It does not run maintainer scripts, start systemd
services, boot an Ubuntu kernel, or validate the complete Packer image.

The Ubuntu cloud root filesystems used for ARM validation are pinned here:

| Release | Archive | SHA-256 |
|---|---|---|
| 24.04 | [20260911 ARM rootfs](https://cloud-images.ubuntu.com/noble/20260911/noble-server-cloudimg-arm64-root.tar.xz) | `8482f421d456576ac5fa1f01a572846b6b7356ee5a4f58b895920e3118c2f9e7` |
| 26.04 | [20260823 ARM rootfs](https://cloud-images.ubuntu.com/releases/resolute/release-20260823/ubuntu-26.04-server-cloudimg-arm64-root.tar.xz) | `c80e92ec6d33ab36558d3ceff3f35c985aa14ca9a556a23187920ce66e226cd5` |

## Smoke test

After the guest accepts SSH, run from the repository root:

```bash
nix run ./os-image#smoke-builder -- --arch arm64 \
  --key "$HOME/.ssh/id_ed25519" --port 2222 --output /tmp/nke-builder-smoke
```

The test compiles and runs a CGo program, builds and extracts a Debian package,
generates its apt index, and copies the package to the host with a checksum
check. Shut down and restart the same VM, then repeat with
`--check-persistence` to verify a stored Nix object, its database registration,
and the Go build cache survived. The smoke test checks ELF linkage but does not execute in Ubuntu; use the
separate runtime check above. Neither test validates the complete NKE image pipeline. Test amd64 on a
native x86 host separately.
