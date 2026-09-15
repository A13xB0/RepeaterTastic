#!/usr/bin/env bash
# Build the RepeaterTastic-patched MeshCore KISS modem firmware.
#
# Usage: ./build.sh [pio-env]            (default env: Heltec_v3_kiss_modem)
#
# Environment:
#   MESHCORE_DIR   existing MeshCore checkout to build in. The patch is applied
#                  if it is not already applied. If unset, MeshCore is cloned
#                  into firmware/work/MeshCore (gitignored).
#   MESHCORE_REPO  git URL to clone from (default: upstream GitHub)
#   MESHCORE_REF   commit to check out after cloning (default: BASE_COMMIT below)
#   PIO            platformio executable (default: pio on PATH, else ~/.local/bin/pio)
#
# Output (firmware/out/*.bin is gitignored): firmware/out/<env>-firmware.bin, and for ESP32 targets
#         firmware/out/<env>-factory.bin (merged image, flash at 0x0).
#         nRF52 targets also get <env>.uf2 / .zip if PlatformIO produced them.
set -euo pipefail

# Upstream meshcore-dev/MeshCore (dev branch) commit the patch was developed on.
# (The local development checkout was b7e5db8d = this commit + an unrelated
# decrypt() bounds fix; the patch applies identically to both.)
BASE_COMMIT="e0031870f6e94657765a77e5f7676654d465dd86"

ENV_NAME="${1:-Heltec_v3_kiss_modem}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PATCH="$SCRIPT_DIR/0001-kiss-modem-sync-word-preamble.patch"
OUT_DIR="$SCRIPT_DIR/out"
MESHCORE_REPO="${MESHCORE_REPO:-https://github.com/meshcore-dev/MeshCore.git}"
MESHCORE_REF="${MESHCORE_REF:-$BASE_COMMIT}"

if [[ -z "${PIO:-}" ]]; then
  if command -v pio >/dev/null 2>&1; then PIO=pio
  elif [[ -x "$HOME/.local/bin/pio" ]]; then PIO="$HOME/.local/bin/pio"
  else echo "error: platformio (pio) not found; set PIO=/path/to/pio" >&2; exit 1
  fi
fi

if [[ -n "${MESHCORE_DIR:-}" ]]; then
  SRC="$MESHCORE_DIR"
else
  SRC="$SCRIPT_DIR/work/MeshCore"
  if [[ ! -d "$SRC/.git" ]]; then
    mkdir -p "$(dirname "$SRC")"
    git clone "$MESHCORE_REPO" "$SRC"
  fi
  if ! git -C "$SRC" cat-file -e "$MESHCORE_REF^{commit}" 2>/dev/null; then
    git -C "$SRC" fetch origin "$MESHCORE_REF" || git -C "$SRC" fetch origin
  fi
  # discard any previous patch application so the tree is exactly base + patch
  git -C "$SRC" checkout -q -f "$MESHCORE_REF"
  git -C "$SRC" clean -q -fd -e .pio
fi

if git -C "$SRC" apply --reverse --check "$PATCH" 2>/dev/null; then
  echo ">> patch already applied in $SRC"
else
  echo ">> applying $(basename "$PATCH")"
  git -C "$SRC" apply "$PATCH"
fi

echo ">> building $ENV_NAME"
(cd "$SRC" && "$PIO" run -e "$ENV_NAME")

BUILD="$SRC/.pio/build/$ENV_NAME"
mkdir -p "$OUT_DIR"

if [[ -f "$BUILD/firmware.bin" ]]; then
  cp "$BUILD/firmware.bin" "$OUT_DIR/$ENV_NAME-firmware.bin"
  echo ">> $OUT_DIR/$ENV_NAME-firmware.bin"
fi

if [[ -f "$BUILD/bootloader.bin" && -f "$BUILD/partitions.bin" ]]; then
  # ESP32: produce a merged factory image (bootloader + partitions + boot_app0 + app)
  echo ">> merging factory image"
  (cd "$SRC" && "$PIO" run -e "$ENV_NAME" -t mergebin)
  cp "$BUILD/firmware-merged.bin" "$OUT_DIR/$ENV_NAME-factory.bin"
  echo ">> $OUT_DIR/$ENV_NAME-factory.bin (flash at 0x0)"
fi

for ext in uf2 zip hex; do
  if [[ -f "$BUILD/firmware.$ext" ]]; then
    cp "$BUILD/firmware.$ext" "$OUT_DIR/$ENV_NAME.$ext"
    echo ">> $OUT_DIR/$ENV_NAME.$ext"
  fi
done

(cd "$OUT_DIR" && sha256sum "$ENV_NAME"* > "$ENV_NAME.sha256")
echo ">> done"
