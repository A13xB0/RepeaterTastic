#!/usr/bin/env python3
"""Idempotently configure meshtasticd instances over the TCP API.

For every HOST:PORT given it ensures:
  * lora.region = EU_868, use_preset = true, modem_preset = LONG_FAST, hop_limit = 3
  * network.enabled_protocols has UDP_BROADCAST (bit 0) set
  * primary channel = default LongFast (empty name, PSK alias 0x01)
  * owner long/short name = "Interop <nodehex>" / last 4 hex digits (or --name-prefix)
  * optional fixed position (--lat/--lon), so position requests get answered
Only fields that differ are written, so re-running is a no-op.

Setting the owner name makes the firmware reboot itself (~7 s); the container
restarts in place. Firmware only generates the X25519 key pair once a region is set, so
the PKI keys appear after the first run.

Usage (host, bridge mode publishes API ports on 127.0.0.1):
  .venv/bin/python setup_nodes.py 127.0.0.1:4410 127.0.0.1:4411
or inside the mesh network:
  ./run_meshtasticd.sh tools setup_nodes.py repeatertastic-mtd-1:4410 repeatertastic-mtd-2:4411
"""

from __future__ import annotations

import argparse
import sys
import time

from meshtastic.protobuf import channel_pb2, config_pb2
from meshtastic.tcp_interface import TCPInterface

EU_868 = config_pb2.Config.LoRaConfig.RegionCode.EU_868
LONG_FAST = config_pb2.Config.LoRaConfig.ModemPreset.LONG_FAST
UDP_BROADCAST = config_pb2.Config.NetworkConfig.ProtocolFlags.UDP_BROADCAST


def connect(target: str, retries: int = 30) -> TCPInterface:
    host, _, port = target.rpartition(":")
    last = None
    for _ in range(retries):
        try:
            return TCPInterface(hostname=host or "127.0.0.1", portNumber=int(port))
        except Exception as e:  # noqa: BLE001  (API not up yet)
            last = e
            time.sleep(1)
    raise SystemExit(f"cannot connect to {target}: {last}")


def configure(target: str, args) -> dict:
    iface = connect(target)
    try:
        node = iface.localNode
        num = iface.myInfo.my_node_num
        changed = []

        lora = node.localConfig.lora
        if not (lora.region == EU_868 and lora.use_preset and lora.modem_preset == LONG_FAST and lora.hop_limit == args.hop_limit
                and lora.tx_enabled):
            lora.region = EU_868
            lora.use_preset = True
            lora.modem_preset = LONG_FAST
            lora.hop_limit = args.hop_limit
            lora.tx_enabled = True
            node.writeConfig("lora")
            changed.append("lora")

        net = node.localConfig.network
        if not net.enabled_protocols & UDP_BROADCAST:
            net.enabled_protocols |= UDP_BROADCAST
            node.writeConfig("network")
            changed.append("network")

        if args.lat is not None and args.lon is not None:
            pos = node.localConfig.position
            if not pos.fixed_position:
                node.setFixedPosition(args.lat, args.lon, args.alt)
                changed.append("fixed_position")

        ch = node.channels[0] if node.channels else None
        if ch is not None and (ch.role != channel_pb2.Channel.Role.PRIMARY or ch.settings.name != "" or ch.settings.psk != b"\x01"):
            ch.role = channel_pb2.Channel.Role.PRIMARY
            ch.settings.name = ""
            ch.settings.psk = b"\x01"
            node.writeChannel(0)
            changed.append("channel0")

        long_name = f"{args.name_prefix} {num:08x}"
        short_name = f"{num & 0xFFFF:04x}"
        me = iface.nodesByNum.get(num, {}).get("user", {})
        if me.get("longName") != long_name or me.get("shortName") != short_name:
            node.setOwner(long_name=long_name, short_name=short_name)
            changed.append("owner")

        time.sleep(2 if changed else 0)
        peers = []
        if args.show_nodes:
            for n, info in sorted(iface.nodesByNum.items()):
                if n == num:
                    continue
                u = info.get("user", {})
                peers.append(f"!{n:08x} name={u.get('longName')!r} pubkey={'yes' if u.get('publicKey') else 'no'} "
                             f"hops_away={info.get('hopsAway')} last_heard={info.get('lastHeard')}")
        return {"target": target, "node": f"!{num:08x}", "changed": changed, "peers": peers}
    finally:
        iface.close()


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("targets", nargs="+", help="HOST:PORT of each meshtasticd TCP API")
    ap.add_argument("--hop-limit", type=int, default=3)
    ap.add_argument("--name-prefix", default="Interop")
    ap.add_argument("--show-nodes", action="store_true", help="print each node's view of the mesh (proves discovery)")
    ap.add_argument("--lat", type=float, default=None, help="fixed latitude (applied to every node, offset by index)")
    ap.add_argument("--lon", type=float, default=None)
    ap.add_argument("--alt", type=int, default=0)
    args = ap.parse_args()

    base_lat, base_lon = args.lat, args.lon
    for i, t in enumerate(args.targets):
        if base_lat is not None:
            args.lat, args.lon = base_lat + i * 0.001, base_lon + i * 0.001
        r = configure(t, args)
        print(f"{r['target']:>28} {r['node']} changed={','.join(r['changed']) or '-'}")
        for peer in r["peers"]:
            print(f"{'':>28}   sees {peer}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
