# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
{
  description = "Direct-boot NixOS package-builder VMs for NKE";

  # Follow nixos-unstable intentionally; flake.lock pins the exact revision used.
  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs =
    { self, nixpkgs }:
    let
      hostSystems = [
        "aarch64-darwin"
        "x86_64-linux"
        "aarch64-linux"
      ];
      guests = {
        amd64 = "x86_64-linux";
        arm64 = "aarch64-linux";
      };
      mkBuilder =
        hostSystem: arch: guestSystem:
        let
          hostPkgs = import nixpkgs { system = hostSystem; };
          guest = nixpkgs.lib.nixosSystem {
            system = guestSystem;
            modules = [
              ./nix/builder.nix
              {
                system.name = "nke-builder-${arch}";
                virtualisation.host.pkgs = hostPkgs;
              }
            ];
          };
          native =
            hostPkgs.stdenv.hostPlatform.parsed.cpu.name == guest.pkgs.stdenv.hostPlatform.parsed.cpu.name;
          machine = if arch == "arm64" then "virt,gic-version=3" else "q35";
        in
        hostPkgs.writeShellApplication {
          name = "nke-builder-${arch}";
          runtimeInputs = [
            hostPkgs.coreutils
            hostPkgs.gnugrep
          ];
          # Runtime wrapper: attach the key/cache disk and choose native acceleration
          # when available, with software emulation as the fallback.
          text = ''
            export NKE_BUILDER_SSH_KEY
            NKE_BUILDER_SSH_KEY=$(realpath "''${NKE_BUILDER_SSH_KEY:?Set NKE_BUILDER_SSH_KEY to a public SSH key file}")
            test -r "$NKE_BUILDER_SSH_KEY"
            state_dir="''${NKE_BUILDER_STATE_DIR:-''${XDG_CACHE_HOME:-$HOME/.cache}/nke/builders/${arch}}"
            mkdir -p "$state_dir"
            export NIX_DISK_IMAGE="''${NIX_DISK_IMAGE:-$state_dir/root.qcow2}"
            accel=tcg
            ${nixpkgs.lib.optionalString native (
              if hostPkgs.stdenv.hostPlatform.isDarwin then
                ''
                  if /usr/sbin/sysctl -n kern.hv_support | grep -q 1; then accel=hvf; fi
                ''
              else
                ''
                  if [[ -r /dev/kvm && -w /dev/kvm ]]; then accel=kvm; fi
                ''
            )}
            accel=''${ACCELERATOR:-$accel}
            case "$accel" in
              tcg) cpu=max ;;
              kvm|hvf) cpu=host ;;
              *) echo "ACCELERATOR must be kvm, hvf, or tcg" >&2; exit 1 ;;
            esac
            export QEMU_NET_OPTS="hostfwd=tcp:127.0.0.1:''${SSH_PORT:-2222}-:22"
            # exec replaces this wrapper with the launcher so signals reach the VM.
            exec ${guest.config.system.build.vm}/bin/run-nke-builder-${arch}-vm \
              -machine ${machine},accel="$accel" -cpu "$cpu" "$@"
          '';
        };
      mkPackageRunner =
        hostSystem: arch:
        let
          pkgs = import nixpkgs { system = hostSystem; };
          launcher = self.packages.${hostSystem}."builder-${arch}";
          ssh = pkgs.writeShellApplication {
            name = "nke-builder-ssh";
            runtimeInputs = [ pkgs.openssh ];
            text = ''
              # Hand control to SSH; no extra wrapper process is needed.
              exec ssh -F /dev/null -p "$SSH_PORT" \
                -i "$NKE_BUILDER_STATE_DIR/client-key" \
                -o IdentityAgent=none -o IdentitiesOnly=yes -o BatchMode=yes \
                -o ConnectTimeout=2 -o StrictHostKeyChecking=accept-new \
                -o HostKeyAlias=nke-package-builder \
                -o "UserKnownHostsFile=$NKE_BUILDER_STATE_DIR/known_hosts" "$@"
            '';
          };
        in
        pkgs.writeShellApplication {
          name = "build-packages-${arch}";
          runtimeInputs = [
            pkgs.coreutils
            pkgs.flock
            ssh
            pkgs.gitMinimal
            pkgs.openssh
            pkgs.rsync
          ];
          text = ''
            arch=${arch}
            launcher=${launcher}/bin/nke-builder-${arch}
            guest_script=${./nix/build-guest.sh}
          ''
          + builtins.readFile ./nix/build-packages.sh;
        };
      mkBootstrap =
        hostSystem: arch:
        let
          hostPkgs = import nixpkgs { system = hostSystem; };
          # Preserve the upstream guest exactly so a fresh host can substitute it.
          stockPkgs = import nixpkgs { system = "aarch64-darwin"; };
          stock =
            if arch == "arm64" then stockPkgs.darwin.linux-builder else stockPkgs.darwin.linux-builder-x86_64;
          runtime = stock.override {
            modules = [
              ({ lib, ... }: {
                nixpkgs.hostPlatform = guests.${arch};
                virtualisation.host.pkgs = lib.mkForce hostPkgs;
                virtualisation.forwardPorts = lib.mkForce [ ];
              })
            ];
          };
        in
        assert
          stock.nixosConfig.system.build.toplevel.drvPath
          == runtime.nixosConfig.system.build.toplevel.drvPath;
        runtime.nixosConfig.system.build.vm;
      mkPackageApp =
        hostSystem: arch:
        let
          pkgs = import nixpkgs { system = hostSystem; };
          runner = mkPackageRunner hostSystem arch;
          bootstrap = mkBootstrap hostSystem arch;
        in
        if !pkgs.stdenv.hostPlatform.isDarwin then
          runner
        else
          pkgs.writeShellApplication {
            name = "build-packages-${arch}";
            runtimeInputs = with pkgs; [
              nix
              coreutils
              flock
              gnused
              jq
              openssh
            ];
            text = ''
              arch=${arch}
              host_system=${hostSystem}
              guest_system=${guests.${arch}}
              runner_drv=${builtins.unsafeDiscardOutputDependency runner.drvPath}
              bootstrap_drv=${builtins.unsafeDiscardOutputDependency bootstrap.drvPath}
              bootstrap_program=${bootstrap.meta.mainProgram}
              bootstrap_host_key=${nixpkgs.lib.escapeShellArg (nixpkgs.lib.removeSuffix "\n" (builtins.readFile "${nixpkgs}/nixos/modules/profiles/keys/ssh_host_ed25519_key.pub"))}
            ''
            + builtins.readFile ./nix/bootstrap-packages.sh;
          };
    in
    {
      packages = nixpkgs.lib.genAttrs hostSystems (
        hostSystem:
        (nixpkgs.lib.mapAttrs' (
          arch: guestSystem:
          nixpkgs.lib.nameValuePair "builder-${arch}" (mkBuilder hostSystem arch guestSystem)
        ) guests)
        # // merges attribute sets: combine VM launchers, build apps, and the default.
        // (nixpkgs.lib.mapAttrs' (
          arch: _: nixpkgs.lib.nameValuePair "build-packages-${arch}" (mkPackageApp hostSystem arch)
        ) guests)
        // {
          smoke-builder =
            let
              pkgs = import nixpkgs { system = hostSystem; };
            in
            pkgs.writeShellApplication {
              name = "smoke-builder";
              runtimeInputs = [
                pkgs.coreutils
                pkgs.openssh
              ];
              text = builtins.readFile ./nix/smoke-builder.sh;
            };
          default =
            self.packages.${hostSystem}.${
              if nixpkgs.lib.hasPrefix "aarch64" hostSystem then "builder-arm64" else "builder-amd64"
            };
        }
      );
      apps = nixpkgs.lib.genAttrs hostSystems (
        hostSystem:
        nixpkgs.lib.mapAttrs' (
          arch: _:
          nixpkgs.lib.nameValuePair "build-packages-${arch}" {
            type = "app";
            program = "${self.packages.${hostSystem}."build-packages-${arch}"}/bin/build-packages-${arch}";
          }
        ) guests
        // {
          smoke-builder = {
            type = "app";
            program = "${self.packages.${hostSystem}.smoke-builder}/bin/smoke-builder";
          };
        }
      );
    };
}
