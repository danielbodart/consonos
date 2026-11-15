#!/usr/bin/env bash
# Evaluates the flake's package and checks for every system CI does not
# build on (aarch64-linux), down to their derivations, without building
# anything, so an evaluation error there does not land unseen. The package
# exists only where nix/release.json does (flake.nix), so CI runs this
# after scripts/nix-local-release.sh has written one.
#
# usage: scripts/nix-eval-systems.sh     (from anywhere in the repository)
set -euo pipefail
unset CDPATH

cd "$(git rev-parse --show-toplevel)"

built=x86_64-linux
names() { nix eval --json ".#$1" --apply builtins.attrNames | jq -r '.[]'; }
mapfile -t packaged < <(names packages)
mapfile -t checked < <(names checks)

for sys in "${packaged[@]}"; do
    [ "$sys" != "$built" ] || continue
    nix eval --raw ".#packages.$sys.default.drvPath" >/dev/null
    echo "$sys: package evaluates"
done
for sys in "${checked[@]}"; do
    [ "$sys" != "$built" ] || continue
    nix eval --json ".#checks.$sys" --apply 'cs: builtins.mapAttrs (_: c: c.drvPath) cs' >/dev/null
    echo "$sys: checks evaluate"
done
