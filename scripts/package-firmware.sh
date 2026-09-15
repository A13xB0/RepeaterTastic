#!/usr/bin/env bash
# Package firmware/out (from firmware/build.sh) as release assets in <dest>:
#   kiss-firmware-<env>.zip for the featured boards, repeatertastic-kiss-firmware-all-boards.zip
#   with every board that built, firmware-boards.md, and FIRMWARE-SHA256SUMS for the lot.
# Usage: scripts/package-firmware.sh <dest>
set -euo pipefail
cd "$(dirname "$0")/.."
dest=$(realpath -m "${1:?usage: scripts/package-firmware.sh <dest>}")
out=firmware/out
featured="Heltec_v3_kiss_modem heltec_v4_kiss_modem RAK_4631_kiss_modem Xiao_nrf52_kiss_modem"
mkdir -p "$dest"
built=()
while IFS=$'\t' read -r env status _; do
  [[ "$status" == OK && -d "$out/$env" ]] && built+=("$env")
done < "$out/status.tsv"
[[ ${#built[@]} -gt 0 ]] || { echo "no firmware built" >&2; exit 1; }
for env in $featured; do
  if [[ -d "$out/$env" ]]; then
    (cd "$out/$env" && zip -qr -X "$dest/kiss-firmware-$env.zip" .)
  else
    echo "::warning::featured board $env didn't build"
  fi
done
(cd "$out" && zip -qr -X "$dest/repeatertastic-kiss-firmware-all-boards.zip" "${built[@]}")
cp firmware/boards.md "$dest/firmware-boards.md"
(cd "$dest" && sha256sum kiss-firmware-*.zip repeatertastic-kiss-firmware-all-boards.zip firmware-boards.md > FIRMWARE-SHA256SUMS)
echo "packaged ${#built[@]} boards into $dest"
