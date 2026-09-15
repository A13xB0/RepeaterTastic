#!/usr/bin/env bash
# Build the RepeaterTastic-patched MeshCore KISS modem firmware for one or more boards.
#
# Usage:
#   ./build.sh                              # Heltec_v3_kiss_modem
#   ./build.sh <env> [<env> ...]            # specific PlatformIO envs (must be listed in boards.txt)
#   ./build.sh all                          # every env in boards.txt
#
# Options (environment variables):
#   PARALLEL=N     number of envs to build concurrently (default 1)
#   PIO_JOBS=N     compiler jobs per env (default: nproc / PARALLEL)
#   CLEAN=1        delete .pio/build/<env> and .pio/libdeps/<env> after a successful
#                  build (a full `all` build otherwise needs ~20 GB)
#   MESHCORE_DIR   existing MeshCore checkout to build in; the patch is applied if it is
#                  not already. If unset, MeshCore is cloned into firmware/work/MeshCore
#                  (gitignored) and reset to BASE_COMMIT + patch.
#   MESHCORE_REPO  git URL to clone (default: upstream GitHub)
#   MESHCORE_REF   commit to check out (default: BASE_COMMIT)
#   PIO            platformio executable (default: pio on PATH, else ~/.local/bin/pio)
#   OUT_DIR        artifact directory (default: firmware/out)
#   PIOARDUINO_CORE_DIR  separate PlatformIO core dir for boards tagged `core=pioarduino`
#                  in boards.txt (default ~/.platformio-pioarduino). The ESP32-C6 envs use the
#                  pioarduino espressif32 fork, which PlatformIO cannot install next to
#                  platformio/espressif32@6.11.0 in one core dir (FileExistsError).
#
# Output (firmware/out/**/*.bin|uf2|zip|hex are build products, not committed):
#   out/<env>/  esp32:  <env>-factory.bin (flash at 0x0) + <env>-app.bin (app partition, 0x10000)
#               nrf52:  <env>.uf2 (UF2 drag & drop) + <env>-dfu.zip (adafruit-nrfutil) + <env>.hex
#               rp2040: <env>.uf2 (BOOTSEL drag & drop) + <env>.bin
#               stm32:  <env>.bin + <env>.hex
#               plus SHA256SUMS
#   out/logs/<env>.log    full build log
#   out/status.tsv        env <TAB> OK|FAILED <TAB> reason
set -uo pipefail

# Upstream meshcore-dev/MeshCore (dev branch) commit the patch is based on.
BASE_COMMIT="e0031870f6e94657765a77e5f7676654d465dd86"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PATCH="$SCRIPT_DIR/0001-kiss-modem-sync-word-preamble.patch"
BOARDS="$SCRIPT_DIR/boards.txt"
OUT_DIR="${OUT_DIR:-$SCRIPT_DIR/out}"
MESHCORE_REPO="${MESHCORE_REPO:-https://github.com/meshcore-dev/MeshCore.git}"
MESHCORE_REF="${MESHCORE_REF:-$BASE_COMMIT}"
PARALLEL="${PARALLEL:-1}"
NPROC="$(nproc 2>/dev/null || echo 4)"
PIO_JOBS="${PIO_JOBS:-$(( NPROC / PARALLEL > 0 ? NPROC / PARALLEL : 1 ))}"
CLEAN="${CLEAN:-0}"

if [[ -z "${PIO:-}" ]]; then
  if command -v pio >/dev/null 2>&1; then PIO=pio
  elif [[ -x "$HOME/.local/bin/pio" ]]; then PIO="$HOME/.local/bin/pio"
  else echo "error: platformio (pio) not found; set PIO=/path/to/pio" >&2; exit 1
  fi
fi

# ---- board list --------------------------------------------------------------------------
# prints "env|name|mcu|radio|format|options" with whitespace trimmed
board_lines() {
  grep -v '^[[:space:]]*#' "$BOARDS" | grep '|' | \
    awk -F'|' '{ for (i=1;i<=NF;i++) { gsub(/^[ \t]+|[ \t]+$/, "", $i) } print $1"|"$2"|"$3"|"$4"|"$5"|"$6 }'
}
# run pio for an env, honouring per-board options (currently: core=pioarduino)
pio_env() {  # pio_env <env> <pio args...>
  local env="$1"; shift
  if [[ "$(board_field "$env" 6)" == *core=pioarduino* ]]; then
    PLATFORMIO_CORE_DIR="${PIOARDUINO_CORE_DIR:-$HOME/.platformio-pioarduino}" "$PIO" "$@"
  else
    "$PIO" "$@"
  fi
}
board_field() {  # board_field <env> <field#>
  board_lines | awk -F'|' -v e="$1" -v f="$2" '$1==e { print $f; exit }'
}

if [[ $# -eq 0 ]]; then
  ENVS=(Heltec_v3_kiss_modem)
elif [[ "$1" == "all" ]]; then
  mapfile -t ENVS < <(board_lines | cut -d'|' -f1)
else
  ENVS=("$@")
fi
for e in "${ENVS[@]}"; do
  if [[ -z "$(board_field "$e" 1)" ]]; then
    echo "error: env '$e' is not listed in boards.txt" >&2; exit 1
  fi
done

# ---- source tree -------------------------------------------------------------------------
if [[ -n "${MESHCORE_DIR:-}" ]]; then
  SRC="$MESHCORE_DIR"
else
  SRC="$SCRIPT_DIR/work/MeshCore"
  if [[ ! -d "$SRC/.git" ]]; then
    mkdir -p "$(dirname "$SRC")"
    git clone "$MESHCORE_REPO" "$SRC" || exit 1
  fi
  if ! git -C "$SRC" cat-file -e "$MESHCORE_REF^{commit}" 2>/dev/null; then
    git -C "$SRC" fetch origin "$MESHCORE_REF" || git -C "$SRC" fetch origin || exit 1
  fi
  # discard any previous patch application so the tree is exactly base + patch
  git -C "$SRC" checkout -q -f "$MESHCORE_REF" || exit 1
  git -C "$SRC" clean -q -fd -e .pio
fi

if git -C "$SRC" apply --reverse --check "$PATCH" 2>/dev/null; then
  echo ">> patch already applied in $SRC"
else
  echo ">> applying $(basename "$PATCH")"
  git -C "$SRC" apply "$PATCH" || { echo "error: patch does not apply to $SRC" >&2; exit 1; }
fi

mkdir -p "$OUT_DIR/logs"
STATUS="$OUT_DIR/status.tsv"
touch "$STATUS"

# ---- per-env build -----------------------------------------------------------------------
set_status() {  # set_status <env> <OK|FAILED> <reason>
  local tmp; tmp="$(mktemp)"
  ( flock 9
    grep -v -P "^\Q$1\E\t" "$STATUS" > "$tmp" || true
    printf '%s\t%s\t%s\n' "$1" "$2" "$3" >> "$tmp"
    sort -o "$STATUS" "$tmp"
  ) 9>"$STATUS.lock"
  rm -f "$tmp"
}

first_error() {  # one-line reason from a build log
  local log="$1" line
  line="$(grep -m1 -E 'error:|Error:|\*\*\* \[|fatal|UnknownPackageError|PlatformioException|Could not' "$log" \
          | sed -E 's/^[[:space:]]+//' | cut -c1-160)"
  echo "${line:-see logs/$(basename "$log")}"
}

build_one() {
  local env="$1" fmt log dest b
  PIOARDUINO_CORE_DIR="${PIOARDUINO_CORE_DIR:-$HOME/.platformio-pioarduino}"
  fmt="$(board_field "$env" 5)"
  log="$OUT_DIR/logs/$env.log"
  dest="$OUT_DIR/$env"
  b="$SRC/.pio/build/$env"
  echo ">> [$env] building ($fmt)"
  rm -rf "$dest"; mkdir -p "$dest"

  if ! (cd "$SRC" && pio_env "$env" run -e "$env" -j "$PIO_JOBS") > "$log" 2>&1; then
    set_status "$env" FAILED "$(first_error "$log")"
    echo "!! [$env] FAILED: $(first_error "$log")"
    rmdir "$dest" 2>/dev/null
    return 1
  fi

  local ok=1
  case "$fmt" in
    esp32)
      cp "$b/firmware.bin" "$dest/$env-app.bin" || ok=0
      if (cd "$SRC" && pio_env "$env" run -e "$env" -t mergebin) >> "$log" 2>&1 && [[ -f "$b/firmware-merged.bin" ]]; then
        cp "$b/firmware-merged.bin" "$dest/$env-factory.bin"
      else
        echo "merge failed" >> "$log"; ok=0
      fi
      ;;
    nrf52)
      [[ -f "$b/firmware.uf2" ]] || (cd "$SRC" && pio_env "$env" run -e "$env" -t create_uf2) >> "$log" 2>&1
      if [[ -f "$b/firmware.uf2" ]]; then cp "$b/firmware.uf2" "$dest/$env.uf2"; else ok=0; fi
      [[ -f "$b/firmware.zip" ]] && cp "$b/firmware.zip" "$dest/$env-dfu.zip"
      [[ -f "$b/firmware.hex" ]] && cp "$b/firmware.hex" "$dest/$env.hex"
      ;;
    rp2040)
      if [[ -f "$b/firmware.uf2" ]]; then cp "$b/firmware.uf2" "$dest/$env.uf2"; else ok=0; fi
      [[ -f "$b/firmware.bin" ]] && cp "$b/firmware.bin" "$dest/$env.bin"
      ;;
    stm32)
      [[ -f "$b/firmware.bin" ]] && cp "$b/firmware.bin" "$dest/$env.bin"
      [[ -f "$b/firmware.hex" ]] && cp "$b/firmware.hex" "$dest/$env.hex"
      [[ -f "$dest/$env.bin" || -f "$dest/$env.hex" ]] || ok=0
      ;;
    *)
      echo "unknown flash format '$fmt'" >> "$log"; ok=0 ;;
  esac

  (cd "$dest" && sha256sum -- * > SHA256SUMS 2>/dev/null)
  if [[ $ok -eq 1 ]]; then
    set_status "$env" OK "$(cd "$dest" && ls -1 | grep -v SHA256SUMS | tr '\n' ' ' | sed 's/ $//')"
    echo ">> [$env] OK"
    if [[ "$CLEAN" == "1" ]]; then rm -rf "$b" "$SRC/.pio/libdeps/$env"; fi
    return 0
  fi
  set_status "$env" FAILED "compiled, but expected $fmt artifacts were not produced"
  echo "!! [$env] FAILED: missing artifacts"
  return 1
}

# ---- run ---------------------------------------------------------------------------------
echo ">> ${#ENVS[@]} env(s), $PARALLEL in parallel, $PIO_JOBS compile jobs each; output in $OUT_DIR"

if [[ "$PARALLEL" -gt 1 ]]; then
  # Install platforms/toolchains serially, one env per (MCU, format) group, so that parallel
  # builds do not race on the shared ~/.platformio/packages directory.
  declare -A seen=()
  for e in "${ENVS[@]}"; do
    key="$(board_field "$e" 3)/$(board_field "$e" 5)/$(board_field "$e" 6)"
    [[ -n "${seen[$key]:-}" ]] && continue
    seen[$key]=1
    echo ">> preparing toolchain for $key via $e"
    (cd "$SRC" && pio_env "$e" pkg install -e "$e") > "$OUT_DIR/logs/$e.pkg.log" 2>&1 || true
  done
  export -f build_one set_status first_error board_field board_lines pio_env
  export SRC OUT_DIR PIO PIO_JOBS CLEAN STATUS BOARDS PIOARDUINO_CORE_DIR
  printf '%s\n' "${ENVS[@]}" | xargs -P "$PARALLEL" -I{} bash -c 'build_one "$@"' _ {}
else
  for e in "${ENVS[@]}"; do build_one "$e"; done
fi

rm -f "$STATUS.lock"
ok=0; failed=0
for e in "${ENVS[@]}"; do
  case "$(awk -F'\t' -v e="$e" '$1==e {print $2}' "$STATUS")" in
    OK) ok=$((ok+1)) ;;
    *) failed=$((failed+1)) ;;
  esac
done
echo ">> done: $ok OK, $failed FAILED (see $STATUS)"
[[ $failed -eq 0 ]]
