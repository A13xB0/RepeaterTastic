#!/usr/bin/env python3
"""Capture golden Meshtastic-over-UDP test vectors from real meshtasticd instances.

Needs two configured instances (run_meshtasticd.sh up 2 + setup_nodes.py, fixed
positions recommended) and must run where it can receive the multicast group:
inside the docker mesh network (bridge mode, the default):

  ./run_meshtasticd.sh tools capture_vectors.py repeatertastic-mtd-1:4410 repeatertastic-mtd-2:4411

or on the host if NET_MODE=host and the host firewall allows UDP 4403 multicast.

Traffic triggered (A = first node, B = second node), all via the TCP API:
  nodeinfo_broadcast   heartbeat nonce=1 ("nodeinfo ping") -> firmware NodeInfo broadcast (want_response)
  nodeinfo_reply       NodeInfo unicast answer (best effort: the firmware throttles NodeInfo replies)
  text_broadcast       A: TEXT_MESSAGE_APP to ^all
  text_broadcast_relayed  the same packet re-emitted onto UDP by B (hop_limit-1, relay_node=B)
  dm_text              A -> B text with want_ack (PKI if both keys are known)
  routing_ack          B's ROUTING_APP ACK for the DM (request_id = DM id)
  routing_ack_zero_hop A's hop_limit=0 ACK of one of B's want_ack/next_hop packets
  position_request     A -> B empty POSITION_APP want_response (channel-encrypted: POSITION is never PKI)
  position_reply       B's fixed position reply (2.7: precision_bits 13; 2.8 default channel precision 0 -> none)
  routing_nak          B's NO_RESPONSE NAK instead of a position reply (2.8)
  telemetry_request    A -> B TELEMETRY_APP want_response (PKI)
  telemetry_reply      B's DeviceMetrics reply (PKI)
  traceroute_request / traceroute_reply   TRACEROUTE_APP (never PKI)

Each vector is written to vectors/<firmware_version>/<name>.json and every
datagram seen is kept in vectors/<firmware_version>/capture.jsonl.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import socket
import struct
import sys
import threading
import time
from pathlib import Path

from cryptography.hazmat.primitives.asymmetric.x25519 import X25519PrivateKey, X25519PublicKey
from meshtastic.protobuf import mesh_pb2, portnums_pb2, telemetry_pb2
from meshtastic.tcp_interface import TCPInterface

import udp_sniff as us

HERE = Path(__file__).resolve().parent
BROADCAST = 0xFFFFFFFF


# --------------------------------------------------------------------------- sniffing
class Recorder:
    def __init__(self, groups):
        self.sock = us.open_socket(groups)
        self.sock.settimeout(0.5)
        # ask for the destination address so we can record which group the firmware sent to
        self.sock.setsockopt(socket.IPPROTO_IP, getattr(socket, "IP_PKTINFO", 8), 1)
        self.items: list[dict] = []
        self.lock = threading.Lock()
        self.stop = False
        self.t = threading.Thread(target=self._run, daemon=True)
        self.t.start()

    def _run(self):
        while not self.stop:
            try:
                raw, anc, _flags, addr = self.sock.recvmsg(4096, 256)
            except socket.timeout:
                continue
            dst = None
            for level, typ, data in anc:
                if level == socket.IPPROTO_IP and typ == getattr(socket, "IP_PKTINFO", 8) and len(data) >= 12:
                    dst = socket.inet_ntoa(data[8:12])  # struct in_pktinfo.ipi_addr
            with self.lock:
                self.items.append({"t": time.time(), "src": addr[0], "dst": dst, "raw": raw})

    def snapshot(self):
        with self.lock:
            return list(self.items)


# --------------------------------------------------------------------------- helpers
def connect(target: str) -> TCPInterface:
    host, _, port = target.rpartition(":")
    for _ in range(30):
        try:
            return TCPInterface(hostname=host, portNumber=int(port))
        except Exception:  # noqa: BLE001
            time.sleep(1)
    raise SystemExit(f"cannot connect to {target}")


def radio_header(p) -> str:
    """16-byte on-air header the firmware would put in front of `encrypted` (derived, NOT on the UDP wire)."""
    flags = (p.hop_limit & 0x07) | (0x08 if p.want_ack else 0) | (0x10 if p.via_mqtt else 0) | ((p.hop_start & 0x07) << 5)
    return struct.pack("<IIIBBBB", p.to, getattr(p, "from"), p.id, flags, p.channel & 0xFF, p.next_hop & 0xFF,
                       p.relay_node & 0xFF).hex()


def payload_json(portnum: int, payload: bytes):
    try:
        return us.payload_to_dict(portnum, payload)
    except Exception:  # noqa: BLE001
        return None


def build_vector(name, description, item, dec: us.Decoded, ctx) -> dict:
    p = dec.packet
    frm = getattr(p, "from")
    v = {
        "name": name,
        "description": description,
        "firmware_version": ctx["firmware_version"],
        "image": ctx["image"],
        "captured_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime(item["t"])),
        "udp": {"src_ip": item["src"], "dst_group": item["dst"], "port": us.PORT, "datagram_hex": item["raw"].hex(),
                "note": "datagram_hex is the complete UDP payload = protobuf-encoded meshtastic.MeshPacket"},
        "from": frm,
        "to": p.to,
        "id": p.id,
        "channel_hash": p.channel,
        "hop_limit": p.hop_limit,
        "hop_start": p.hop_start,
        "want_ack": p.want_ack,
        "via_mqtt": p.via_mqtt,
        "next_hop": p.next_hop,
        "relay_node": p.relay_node,
        "priority": p.priority,
        "priority_name": mesh_pb2.MeshPacket.Priority.Name(p.priority),
        "transport_mechanism": p.transport_mechanism,
        "rx_time": p.rx_time,
        "rx_snr": p.rx_snr,
        "rx_rssi": p.rx_rssi,
        "pki_encrypted": p.pki_encrypted,
        "public_key_hex": p.public_key.hex(),
        "delayed": p.delayed,
        "all_set_fields": sorted(fd.name for fd, _ in p.ListFields()),
        "encrypted_hex": p.encrypted.hex(),
        "derived_radio_header_hex": radio_header(p),
        "plaintext_hex": dec.plaintext.hex() if dec.plaintext is not None else None,
    }
    if dec.crypto == "channel":
        v["crypto"] = {
            "type": "channel_aes_ctr",
            "channel_name": dec.channel_name,
            "channel_key_hex": dec.channel_key.hex(),
            "channel_psk_alias": 1,
            "channel_hash": us.channel_hash(dec.channel_name, dec.channel_key),
            "iv_hex": us.ctr_nonce(p.id, frm).hex(),
            "note": "AES-%d-CTR, IV = id(u64 LE) | from(u32 LE) | 0(u32); plaintext = Data protobuf" % (len(dec.channel_key) * 8),
        }
        v["channel_key_hex"] = dec.channel_key.hex()
    elif dec.crypto == "pki":
        snd, rcv = ctx["nodes"][frm], ctx["nodes"][p.to]
        shared_raw = X25519PrivateKey.from_private_bytes(rcv["priv"]).exchange(X25519PublicKey.from_public_bytes(snd["pub"]))
        extra = p.encrypted[-4:]
        v["crypto"] = {
            "type": "pki_x25519_aes_ccm",
            "sender_private_key_hex": snd["priv"].hex(),
            "sender_public_key_hex": snd["pub"].hex(),
            "receiver_private_key_hex": rcv["priv"].hex(),
            "receiver_public_key_hex": rcv["pub"].hex(),
            "x25519_shared_secret_hex": shared_raw.hex(),
            "aes_key_hex": hashlib.sha256(shared_raw).digest().hex(),
            "extra_nonce_hex": extra.hex(),
            "extra_nonce_u32_le": struct.unpack("<I", extra)[0],
            "ccm_nonce_hex": us.pki_nonce(p.id, frm, struct.unpack("<I", extra)[0]).hex(),
            "ciphertext_hex": p.encrypted[:-12].hex(),
            "tag_hex": p.encrypted[-12:-4].hex(),
            "note": "key = SHA256(X25519(priv, peer_pub)); AES-256-CCM, M=8 tag, L=2 (13 byte nonce: id u32 LE | extra_nonce u32 LE | from u32 LE | 0x00); encrypted = ciphertext | tag(8) | extra_nonce(4)",
        }
    else:
        v["crypto"] = {"type": dec.crypto}
    if dec.data is not None:
        d = dec.data
        v["decoded"] = {
            "portnum": d.portnum,
            "portnum_name": portnums_pb2.PortNum.Name(d.portnum) if d.portnum in portnums_pb2.PortNum.values() else None,
            "payload_hex": d.payload.hex(),
            "want_response": d.want_response,
            "dest": d.dest,
            "source": d.source,
            "request_id": d.request_id,
            "reply_id": d.reply_id,
            "emoji": d.emoji,
            "bitfield": d.bitfield if d.HasField("bitfield") else None,
            "all_set_fields": sorted(fd.name for fd, _ in d.ListFields()),
            "payload": payload_json(d.portnum, d.payload),
        }
    return v


# --------------------------------------------------------------------------- main flow
def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("node_a", help="HOST:PORT of node A API")
    ap.add_argument("node_b", help="HOST:PORT of node B API")
    ap.add_argument("--out", default=str(HERE / "vectors"))
    ap.add_argument("--image", default="(unknown)", help="docker image tag, recorded in vectors")
    ap.add_argument("--group", action="append")
    ap.add_argument("--settle", type=float, default=12.0, help="seconds to wait after each action")
    ap.add_argument("--nodeinfo-timeout", type=float, default=150.0, help="max seconds to wait for mutual key exchange")
    args = ap.parse_args()

    rec = Recorder(args.group or list(us.GROUPS))
    ia, ib = connect(args.node_a), connect(args.node_b)
    fw = ia.metadata.firmware_version if ia.metadata else "unknown"
    nodes = {}
    for i in (ia, ib):
        s = i.localNode.localConfig.security
        nodes[i.myInfo.my_node_num] = {"priv": bytes(s.private_key), "pub": bytes(s.public_key)}
    A, B = ia.myInfo.my_node_num, ib.myInfo.my_node_num
    print(f"firmware {fw}  A=!{A:08x}  B=!{B:08x}", file=sys.stderr)
    for n, k in nodes.items():
        if len(k["priv"]) != 32:
            print(f"WARNING: !{n:08x} has no PKI key (region unset?) - run setup_nodes.py first", file=sys.stderr)

    kr = us.Keyring()
    for n, k in nodes.items():
        if len(k["priv"]) == 32:
            kr.private_keys[n] = k["priv"]
            kr.public_keys[n] = k["pub"]
    ctx = {"firmware_version": fw, "image": args.image, "nodes": nodes}
    marks: dict[str, float] = {}

    def mark(label):
        marks[label] = time.time()
        print(f"-- {label}", file=sys.stderr)

    def ping(iface):
        tr = mesh_pb2.ToRadio()
        tr.heartbeat.nonce = 1  # "nodeinfo ping": NodeInfo broadcast with want_response, 60 s cooldown
        iface._sendToRadio(tr)  # noqa: SLF001

    def keys_known():
        ka = (ia.nodesByNum.get(B) or {}).get("user", {}).get("publicKey")
        kb = (ib.nodesByNum.get(A) or {}).get("user", {}).get("publicKey")
        return bool(ka), bool(kb)

    # 1. NodeInfo broadcasts (B first so that A's reply is a genuine nodeinfo_reply), retried
    #    because the firmware throttles NodeInfo (60 s for pings, 600 s for replies, persisted).
    mark("nodeinfo")
    deadline = time.time() + args.nodeinfo_timeout
    while True:
        ping(ib)
        time.sleep(3)
        ping(ia)
        time.sleep(args.settle)
        ka, kb = keys_known()
        print(f"A knows B key: {ka}  B knows A key: {kb}", file=sys.stderr)
        if (ka and kb) or time.time() > deadline:
            break
        time.sleep(20)

    mark("text_broadcast")
    txt_bc = ia.sendText("RepeaterTastic golden broadcast")
    time.sleep(args.settle)

    mark("dm")
    acked = threading.Event()
    dm = ia.sendData("RepeaterTastic golden DM".encode(), destinationId=B, portNum=portnums_pb2.TEXT_MESSAGE_APP,
                     wantAck=True, onResponse=lambda p: acked.set(), onResponseAckPermitted=True)
    acked.wait(30)
    time.sleep(args.settle / 2)

    print(f"DM ack received: {acked.is_set()}", file=sys.stderr)

    mark("position")
    pos = ia.sendData(mesh_pb2.Position(), destinationId=B, portNum=portnums_pb2.POSITION_APP, wantResponse=True)
    time.sleep(args.settle)

    mark("telemetry")
    tel = ia.sendData(telemetry_pb2.Telemetry(device_metrics=telemetry_pb2.DeviceMetrics()), destinationId=B,
                      portNum=portnums_pb2.TELEMETRY_APP, wantResponse=True)
    time.sleep(args.settle)

    mark("traceroute")
    trc = ia.sendData(mesh_pb2.RouteDiscovery(), destinationId=B, portNum=portnums_pb2.TRACEROUTE_APP, wantResponse=True)
    time.sleep(args.settle)
    mark("end")

    ia.close()
    ib.close()
    rec.stop = True
    items = rec.snapshot()

    # ------------------------------------------------------------------ decode + select
    decoded = []
    for it in items:
        try:
            dec = us.decode_datagram(it["raw"], kr)
        except Exception as e:  # noqa: BLE001
            print(f"undecodable datagram: {e}", file=sys.stderr)
            continue
        decoded.append((it, dec))

    outdir = Path(args.out) / fw
    outdir.mkdir(parents=True, exist_ok=True)
    with open(outdir / "capture.jsonl", "w") as f:
        for it, dec in decoded:
            p = dec.packet
            f.write(json.dumps({
                "t": it["t"], "src": it["src"], "dst": it["dst"], "datagram_hex": it["raw"].hex(), "from": getattr(p, "from"), "to": p.to,
                "id": p.id, "channel_hash": p.channel, "hop_limit": p.hop_limit, "hop_start": p.hop_start,
                "relay_node": p.relay_node, "next_hop": p.next_hop, "want_ack": p.want_ack,
                "transport_mechanism": p.transport_mechanism, "crypto": dec.crypto,
                "portnum": dec.data.portnum if dec.data is not None else None,
                "request_id": dec.data.request_id if dec.data is not None else None,
                "plaintext_hex": dec.plaintext.hex() if dec.plaintext is not None else None,
            }) + "\n")

    def find(pred, after=None, original=True):
        for it, dec in decoded:
            if after and it["t"] < marks[after]:
                continue
            p = dec.packet
            if dec.data is None:
                continue
            if original and p.relay_node != (getattr(p, "from") & 0xFF):
                continue
            if pred(p, dec.data):
                return it, dec
        return None

    want = []
    frm = lambda p: getattr(p, "from")  # noqa: E731
    want.append(("nodeinfo_broadcast", "Firmware NodeInfo broadcast (User payload incl. public_key), triggered by ToRadio.heartbeat nonce=1",
                 find(lambda p, d: p.to == BROADCAST and d.portnum == portnums_pb2.NODEINFO_APP, "nodeinfo")))
    want.append(("nodeinfo_reply", "(best effort, firmware throttles NodeInfo replies 600 s, persisted across reboots) NodeInfo unicast reply to a NodeInfo with want_response (request_id set, channel-encrypted: NODEINFO never PKI)",
                 find(lambda p, d: p.to != BROADCAST and d.portnum == portnums_pb2.NODEINFO_APP, "nodeinfo")))
    want.append(("text_broadcast", "TEXT_MESSAGE_APP broadcast on default LongFast channel, as originated by A",
                 find(lambda p, d: p.id == txt_bc.id and frm(p) == A, "text_broadcast")))
    want.append(("text_broadcast_relayed", "Same text broadcast as re-emitted onto UDP by B after hearing it over UDP (hop_limit decremented, relay_node = B's last byte)",
                 find(lambda p, d: p.id == txt_bc.id and p.relay_node == (B & 0xFF), "text_broadcast", original=False)))
    want.append(("dm_text", "Direct TEXT_MESSAGE_APP A->B with want_ack (PKI-encrypted when both public keys are known, channel hash 0)",
                 find(lambda p, d: p.id == dm.id, "dm")))
    want.append(("routing_ack", "ROUTING_APP ACK from B for the DM (Routing{error_reason=NONE}, request_id = DM id)",
                 find(lambda p, d: frm(p) == B and d.portnum == portnums_pb2.ROUTING_APP and d.request_id == dm.id, "dm")))
    want.append(("routing_ack_zero_hop", "ROUTING_APP ACK with hop_limit=0/hop_start=0 sent by A back to B for B's want_ack/next_hop reply (request_id = acked packet id)",
                 find(lambda p, d: frm(p) == A and d.portnum == portnums_pb2.ROUTING_APP and p.hop_limit == 0 and d.request_id, "dm")))
    want.append(("position_request", "Empty POSITION_APP request A->B with want_response (position is never PKI)",
                 find(lambda p, d: p.id == pos.id, "position")))
    want.append(("position_reply", "B's POSITION_APP reply (fixed position) with request_id = request id",
                 find(lambda p, d: frm(p) == B and d.portnum == portnums_pb2.POSITION_APP and d.request_id == pos.id, "position")))
    def is_nak(p, d):
        if d.portnum != portnums_pb2.ROUTING_APP:
            return False
        r = mesh_pb2.Routing()
        r.ParseFromString(d.payload)
        return r.WhichOneof("variant") == "error_reason" and r.error_reason != mesh_pb2.Routing.Error.NONE

    want.append(("routing_nak", "(only if it happens) ROUTING_APP NAK, e.g. NO_RESPONSE from B when its channel position_precision is 0 (2.8 default)",
                 find(is_nak, "position")))
    want.append(("telemetry_request", "TELEMETRY_APP request A->B with want_response",
                 find(lambda p, d: p.id == tel.id, "telemetry")))
    want.append(("telemetry_reply", "B's TELEMETRY_APP DeviceMetrics reply",
                 find(lambda p, d: frm(p) == B and d.portnum == portnums_pb2.TELEMETRY_APP and d.request_id == tel.id, "telemetry")))
    want.append(("traceroute_request", "TRACEROUTE_APP request A->B (RouteDiscovery, never PKI)",
                 find(lambda p, d: p.id == trc.id, "traceroute")))
    want.append(("traceroute_reply", "TRACEROUTE_APP reply from B (RouteDiscovery with route/snr lists)",
                 find(lambda p, d: frm(p) == B and d.portnum == portnums_pb2.TRACEROUTE_APP and d.request_id == trc.id, "traceroute")))

    index = []
    for name, desc, hit in want:
        if hit is None:
            print(f"MISSING {name}", file=sys.stderr)
            continue
        it, dec = hit
        v = build_vector(name, desc, it, dec, ctx)
        (outdir / f"{name}.json").write_text(json.dumps(v, indent=2) + "\n")
        index.append({"name": name, "file": f"{fw}/{name}.json", "crypto": v["crypto"]["type"],
                      "portnum": v.get("decoded", {}).get("portnum_name")})
        print(f"wrote {name:24} crypto={v['crypto']['type']:20} id=0x{v['id']:08x}", file=sys.stderr)

    # rebroadcast / echo statistics: how often did we see each packet id, from which source IPs
    seen: dict[int, set] = {}
    for it, dec in decoded:
        seen.setdefault(dec.packet.id, set()).add((it["src"], dec.packet.hop_limit, dec.packet.relay_node))
    nodes_out = {f"!{n:08x}": {"node_num": n, "private_key_hex": k["priv"].hex(), "public_key_hex": k["pub"].hex()}
                 for n, k in nodes.items()}
    groups_seen = sorted({it["dst"] for it, _ in decoded if it.get("dst")})
    notes = [
        "UDP payload is a bare protobuf MeshPacket, always the `encrypted` variant (receivers drop `decoded`).",
        "channel = 8-bit channel hash for channel traffic, 0 for PKI DMs; rx_snr/rx_rssi/rx_time are not set.",
        "Firmware >= 2.8 derives node_num = crc32(public_key) once keys exist (hwid only seeds the first boot); 2.7 uses MAC bytes 2..5.",
        "Packets heard over UDP are re-emitted onto UDP by other nodes (hop_limit-1, relay_node=relayer, transport_mechanism=6).",
    ]
    meta = {"firmware_version": fw, "image": args.image, "multicast_groups_seen": groups_seen, "notes": notes,
            "nodes": nodes_out, "vectors": index,
            "datagrams_total": len(decoded),
            "packet_copies": {f"0x{i:08x}": sorted([list(x) for x in s]) for i, s in seen.items()}}
    (outdir / "index.json").write_text(json.dumps(meta, indent=2) + "\n")
    print(f"{len(index)} vectors -> {outdir}", file=sys.stderr)


if __name__ == "__main__":
    main()
