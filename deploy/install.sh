#!/usr/bin/env bash
# Install RepeaterTastic as a systemd service.
#
#   sudo ./deploy/install.sh ./dist/repeatertastic-linux-arm64 [./dist/kisstool-linux-arm64]
#
# Creates the repeatertastic user (in dialout for the serial modem), installs the binary to
# /usr/local/bin, a config to /etc/repeatertastic if none exists, and enables the service.
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BIN="${1:?usage: install.sh <repeatertastic binary> [kisstool binary]}"
KISSTOOL="${2:-}"

[[ $EUID -eq 0 ]] || { echo "run as root (sudo)" >&2; exit 1; }

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
sleep 1
systemctl --no-pager --lines=15 status repeatertastic || true
ip=$(hostname -I 2>/dev/null | awk '{print $1}')
echo
echo "Open http://${ip:-<this-host>}:8080 to finish setup."
