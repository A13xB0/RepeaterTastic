#!/usr/bin/env bash
# Install RepeaterTastic and meshtasticd as a systemd service on a Raspberry Pi or other Debian-based
# machine (Raspberry Pi OS, Debian 12/13, Ubuntu).
#
#   sudo ./deploy/install.sh                                  # latest release for this machine
#   sudo ./deploy/install.sh <repeatertastic binary> [kisstool binary]   # your own build
#
# It installs meshtasticd 2.8+ from Meshtastic's package repository (unless it's already there) and
# turns meshtasticd's own service off: RepeaterTastic starts its own instances. Then it creates the
# repeatertastic user (in dialout for the serial modem), installs the binaries to /usr/local/bin, a
# config to /etc/repeatertastic if none exists, and enables the service.
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RELEASE=https://github.com/ScotMesh/RepeaterTastic/releases/latest/download
BIN="${1:-}"
KISSTOOL="${2:-}"

[[ $EUID -eq 0 ]] || { echo "run as root (sudo)" >&2; exit 1; }
command -v apt-get >/dev/null || { echo "this installer needs a Debian-based system (apt)" >&2; exit 1; }
. /etc/os-release

# meshtasticd_ok reports whether the meshtasticd on the PATH is 2.8 or newer.
meshtasticd_ok() {
  command -v meshtasticd >/dev/null || return 1
  local v
  v=$(meshtasticd --version 2>/dev/null | grep -oE '[0-9]+\.[0-9]+\.[0-9]+' | head -n1)
  [[ -n "$v" ]] && printf '2.8.0\n%s\n' "$v" | sort -V -C
}

install_meshtasticd() {
  local arch repo url
  arch=$(dpkg --print-architecture)
  case "$ID" in
    ubuntu)
      apt-get install -y software-properties-common
      add-apt-repository -y ppa:meshtastic/alpha
      ;;
    debian|raspbian)
      # 2.8 is in the alpha channel. 32-bit Raspberry Pi OS uses the Raspbian repository.
      repo="Debian_${VERSION_ID}"
      [[ "$ID" == raspbian || ( "$arch" == armhf && -f /etc/rpi-issue ) ]] && repo="Raspbian_${VERSION_ID}"
      url="https://download.opensuse.org/repositories/network:/Meshtastic:/alpha/${repo}"
      apt-get install -y curl gpg
      curl -fsSL "$url/Release.key" | gpg --dearmor >/etc/apt/trusted.gpg.d/network_Meshtastic_alpha.gpg
      echo "deb $url/ /" >/etc/apt/sources.list.d/network:Meshtastic:alpha.list
      ;;
    *)
      echo "no meshtasticd package for $PRETTY_NAME: install meshtasticd 2.8+ yourself, then run this again" >&2
      exit 1
      ;;
  esac
  apt-get update
  apt-get install -y meshtasticd
}

# download fetches a release binary for this machine and checks it against SHA256SUMS.
download() {
  local name="$1-linux-$2" dir="$3"
  curl -fsSL -o "$dir/$name" "$RELEASE/$name"
  (cd "$dir" && grep " $name\$" SHA256SUMS | sha256sum -c --quiet -)
  echo "$dir/$name"
}

if meshtasticd_ok; then
  echo "using $(command -v meshtasticd) $(meshtasticd --version 2>/dev/null | head -n1)"
else
  install_meshtasticd
  meshtasticd_ok || { echo "meshtasticd 2.8 or newer didn't install" >&2; exit 1; }
fi
# RepeaterTastic starts its own instances: the package's service would take the radio and port 4403.
systemctl disable --now meshtasticd 2>/dev/null || true

if [[ -z "$BIN" ]]; then
  case "$(uname -m)" in
    aarch64|arm64) arch=arm64 ;;
    x86_64|amd64) arch=amd64 ;;
    armv7l) arch=armv7 ;;
    armv6l) arch=armv6 ;;
    *) echo "no release build for $(uname -m): build it (make build) and pass the binary" >&2; exit 1 ;;
  esac
  command -v curl >/dev/null || apt-get install -y curl
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT
  curl -fsSL -o "$tmp/SHA256SUMS" "$RELEASE/SHA256SUMS"
  BIN=$(download repeatertastic "$arch" "$tmp")
  KISSTOOL=$(download kisstool "$arch" "$tmp")
fi

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

install -m 0644 "$HERE/repeatertastic.service" /etc/systemd/system/repeatertastic.service
systemctl daemon-reload
systemctl enable --now repeatertastic
systemctl restart repeatertastic # picks up a new binary on a reinstall
sleep 1
systemctl --no-pager --lines=15 status repeatertastic || true
ip=$(hostname -I 2>/dev/null | awk '{print $1}')
echo
echo "Open http://${ip:-<this-host>}:8080 to finish setup."
