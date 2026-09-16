#!/usr/bin/env bash
# Install RepeaterTastic as a systemd service, with the meshtasticd it runs its nodes on.
#
#   sudo ./deploy/install.sh [--meshtasticd auto|apt|docker|skip] <repeatertastic binary> [kisstool binary]
#
# Creates the repeatertastic user (in dialout for the serial modem), installs the binary to
# /usr/local/bin, a config to /etc/repeatertastic if none exists, and enables the service.
#
# RepeaterTastic runs every relay persona and identity as its own meshtasticd (2.8.0 or newer):
#   apt     installs the meshtasticd package from Meshtastic's alpha repository (Debian, Raspberry
#           Pi OS, Ubuntu) and turns its own service off, so it doesn't take the radio or port 4403
#   docker  uses Docker instead: pulls the image and lets the service use Docker
#   skip    leaves it to you: install meshtasticd your way, then pick it in the setup wizard
#   auto    (default) skip when a meshtasticd 2.8+ is on the PATH, else apt where it can
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
IMAGE=meshtastic/meshtasticd:2.8.0.47db0e3-alpha-debian
MODE=auto
if [[ "${1:-}" == --meshtasticd ]]; then
  MODE="${2:?--meshtasticd needs auto, apt, docker or skip}"
  shift 2
elif [[ "${1:-}" == --meshtasticd=* ]]; then
  MODE="${1#*=}"
  shift
fi
BIN="${1:?usage: install.sh [--meshtasticd auto|apt|docker|skip] <repeatertastic binary> [kisstool binary]}"
KISSTOOL="${2:-}"

[[ $EUID -eq 0 ]] || { echo "run as root (sudo)" >&2; exit 1; }
case "$MODE" in auto|apt|docker|skip) ;; *) echo "--meshtasticd must be auto, apt, docker or skip" >&2; exit 1 ;; esac

# meshtasticd_ok reports whether the meshtasticd on the PATH is 2.8 or newer.
meshtasticd_ok() {
  command -v meshtasticd >/dev/null || return 1
  local v
  v=$(meshtasticd --version 2>/dev/null | grep -oE '[0-9]+\.[0-9]+\.[0-9]+' | head -n1)
  [[ -n "$v" ]] && printf '2.8.0\n%s\n' "$v" | sort -V -C
}

install_apt() {
  . /etc/os-release
  local arch repo
  arch=$(dpkg --print-architecture)
  case "$ID" in
    ubuntu)
      apt-get install -y software-properties-common
      add-apt-repository -y ppa:meshtastic/alpha
      ;;
    debian|raspbian)
      # Raspberry Pi OS: 32-bit builds come from the Raspbian repository, 64-bit from Debian's.
      repo="Debian_${VERSION_ID}"
      [[ "$ID" == raspbian || ( "$arch" == armhf && -f /etc/rpi-issue ) ]] && repo="Raspbian_${VERSION_ID}"
      local url="https://download.opensuse.org/repositories/network:/Meshtastic:/alpha/${repo}"
      apt-get install -y curl gpg
      curl -fsSL "$url/Release.key" | gpg --dearmor >/etc/apt/trusted.gpg.d/network_Meshtastic_alpha.gpg
      echo "deb $url/ /" >/etc/apt/sources.list.d/network:Meshtastic:alpha.list
      ;;
    *)
      echo "no meshtasticd package for $PRETTY_NAME: install meshtasticd 2.8+ yourself or use --meshtasticd docker" >&2
      return 1
      ;;
  esac
  apt-get update
  apt-get install -y meshtasticd
  # RepeaterTastic starts its own instances: the package's service would take the radio and port 4403.
  systemctl disable --now meshtasticd 2>/dev/null || true
}

install_docker() {
  command -v docker >/dev/null || { echo "Docker isn't installed: see https://docs.docker.com/engine/install/" >&2; return 1; }
  docker pull "$IMAGE"
  # The service runs meshtasticd containers, so it needs Docker's group (which is as good as root).
  install -d /etc/systemd/system/repeatertastic.service.d
  printf '[Service]\nSupplementaryGroups=docker\n' >/etc/systemd/system/repeatertastic.service.d/docker.conf
}

id repeatertastic >/dev/null 2>&1 || useradd --system --home /var/lib/repeatertastic --shell /usr/sbin/nologin repeatertastic
usermod -aG dialout repeatertastic

install -m 0755 "$BIN" /usr/local/bin/repeatertastic
[[ -n "$KISSTOOL" ]] && install -m 0755 "$KISSTOOL" /usr/local/bin/kisstool

install -d -m 0750 -o repeatertastic -g repeatertastic /etc/repeatertastic
if [[ ! -f /etc/repeatertastic/repeatertastic.yaml ]]; then
  install -m 0640 -o repeatertastic -g repeatertastic "$HERE/repeatertastic.example.yaml" /etc/repeatertastic/repeatertastic.yaml
  # Point at the first USB serial device we can find.
  dev=$(ls /dev/serial/by-id/* 2>/dev/null | head -n1 || true)
  [[ -n "$dev" ]] && sed -i "s|device: /dev/ttyUSB0|device: $dev|" /etc/repeatertastic/repeatertastic.yaml
  echo "wrote /etc/repeatertastic/repeatertastic.yaml (device: ${dev:-/dev/ttyUSB0})"
fi

if [[ "$MODE" == auto ]]; then
  if meshtasticd_ok; then
    MODE=skip
    echo "using $(command -v meshtasticd) ($(meshtasticd --version 2>/dev/null | head -n1))"
  elif command -v apt-get >/dev/null; then
    MODE=apt
  else
    MODE=skip
    echo "no meshtasticd 2.8+ found and no apt: install it yourself, or rerun with --meshtasticd docker" >&2
  fi
fi
case "$MODE" in
  apt) install_apt ;;
  docker) install_docker ;;
esac

install -m 0644 "$HERE/repeatertastic.service" /etc/systemd/system/repeatertastic.service
systemctl daemon-reload
systemctl enable --now repeatertastic
systemctl restart repeatertastic # picks up a new binary or drop-in on a reinstall
sleep 1
systemctl --no-pager --lines=15 status repeatertastic || true
ip=$(hostname -I 2>/dev/null | awk '{print $1}')
echo
echo "Open http://${ip:-<this-host>}:8080 to finish setup."
[[ "$MODE" == docker ]] && echo "In the setup wizard's meshtasticd step, choose Docker."
exit 0
