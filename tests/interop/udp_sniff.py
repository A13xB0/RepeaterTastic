#!/usr/bin/env python3
"""Standalone Meshtastic UDP-multicast sniffer.

Joins the Meshtastic multicast group(s) on port 4403, decodes every datagram as a
``meshtastic.MeshPacket`` protobuf, decrypts channel traffic (AES-CTR) with the
default LongFast key (plus any ``--psk`` given) and, if private keys are supplied
with ``--privkey``, decrypts PKI direct messages (X25519 + SHA-256 + AES-CCM).

Group addresses: firmware <= 2.7.x uses 224.0.0.69, firmware >= 2.8.0 uses
239.0.0.69. By default both are joined.

Deps: protobuf + cryptography + the ``meshtastic`` pip package (for its generated
protobufs only). Runs from tests/interop/.venv or the hopstatic-interop-tools image.

Also importable: ``decode_datagram()`` / ``channel_hash()`` / ``decrypt_ctr()`` /
``decrypt_pki()`` are used by capture_vectors.py.
"""

from __future__ import annotations

import argparse
import base64
import hashlib
import json
import socket
import struct
import sys
import time
from dataclasses import dataclass, field

from cryptography.hazmat.primitives.asymmetric.x25519 import X25519PrivateKey, X25519PublicKey
from cryptography.hazmat.primitives.ciphers import Cipher, algorithms, modes
from cryptography.hazmat.primitives.ciphers.aead import AESCCM
from cryptography.exceptions import InvalidTag

from meshtastic.protobuf import admin_pb2, mesh_pb2, portnums_pb2, telemetry_pb2  # generated protobufs only

from google.protobuf.json_format import MessageToDict

PORT = 4403
GROUPS = ("239.0.0.69", "224.0.0.69")
DEFAULT_PSK = bytes.fromhex("d4f1bb3a20290759f0bcffabcf4e6901")
PKC_OVERHEAD = 12  # 8 byte CCM tag + 4 byte extra nonce


def expand_psk(psk: bytes) -> bytes:
    """Firmware PSK aliasing: 1 byte index -> default key with last byte += index-1."""
    if len(psk) == 0:
        return b""
    if len(psk) == 1:
        idx = psk[0]
        if idx == 0:
            return b""  # no crypto
        k = bytearray(DEFAULT_PSK)
        k[-1] = (k[-1] + idx - 1) & 0xFF
        return bytes(k)
    return psk


def xor_hash(b: bytes) -> int:
    h = 0
    for x in b:
        h ^= x
    return h


def channel_hash(name: str, key: bytes) -> int:
    return xor_hash(name.encode()) ^ xor_hash(key)


def ctr_nonce(packet_id: int, from_node: int) -> bytes:
    return struct.pack("<QI", packet_id, from_node) + b"\x00" * 4


def decrypt_ctr(key: bytes, packet_id: int, from_node: int, data: bytes) -> bytes:
    """AES-CTR (128 or 256), 16-byte IV = id u64 LE | from u32 LE | 0u32; whole block is counter."""
    c = Cipher(algorithms.AES(key), modes.CTR(ctr_nonce(packet_id, from_node))).decryptor()
    return c.update(data) + c.finalize()


def pki_shared_key(my_private: bytes, their_public: bytes) -> bytes:
    shared = X25519PrivateKey.from_private_bytes(my_private).exchange(X25519PublicKey.from_public_bytes(their_public))
    return hashlib.sha256(shared).digest()


def pki_nonce(packet_id: int, from_node: int, extra_nonce: int) -> bytes:
    n = bytearray(16)
    n[0:8] = struct.pack("<Q", packet_id)
    n[8:12] = struct.pack("<I", from_node)
    if extra_nonce:
        n[4:8] = struct.pack("<I", extra_nonce)
    return bytes(n[:13])  # CCM with L=2 -> 13 byte nonce


def decrypt_pki(shared_key: bytes, packet_id: int, from_node: int, data: bytes) -> bytes:
    """Layout: ciphertext | 8 byte tag | 4 byte extraNonce (LE)."""
    if len(data) <= PKC_OVERHEAD:
        raise ValueError("too short for PKC")
    ct, tag, extra = data[:-12], data[-12:-4], data[-4:]
    nonce = pki_nonce(packet_id, from_node, struct.unpack("<I", extra)[0])
    return AESCCM(shared_key, tag_length=8).decrypt(nonce, ct + tag, None)


PAYLOAD_TYPES = {
    portnums_pb2.NODEINFO_APP: mesh_pb2.User,
    portnums_pb2.POSITION_APP: mesh_pb2.Position,
    portnums_pb2.ROUTING_APP: mesh_pb2.Routing,
    portnums_pb2.TELEMETRY_APP: telemetry_pb2.Telemetry,
    portnums_pb2.TRACEROUTE_APP: mesh_pb2.RouteDiscovery,
    portnums_pb2.ADMIN_APP: admin_pb2.AdminMessage,
    portnums_pb2.NEIGHBORINFO_APP: mesh_pb2.NeighborInfo,
}


def payload_to_dict(portnum: int, payload: bytes):
    if portnum in (portnums_pb2.TEXT_MESSAGE_APP, portnums_pb2.DETECTION_SENSOR_APP, portnums_pb2.ALERT_APP):
        return payload.decode("utf-8", "replace")
    t = PAYLOAD_TYPES.get(portnum)
    if t is None:
        return None
    m = t()
    try:
        m.ParseFromString(payload)
    except Exception as e:  # noqa: BLE001
        return f"<parse error {e}>"
    d = MessageToDict(m, preserving_proto_field_name=True)
    # MessageToDict renders bytes as base64; hex is nicer for crypto work
    if portnum == portnums_pb2.NODEINFO_APP and m.public_key:
        d["public_key"] = m.public_key.hex()
    if portnum == portnums_pb2.NODEINFO_APP and m.macaddr:
        d["macaddr"] = m.macaddr.hex()
    return d


@dataclass
class Keyring:
    channels: list[tuple[str, bytes]] = field(default_factory=lambda: [("LongFast", DEFAULT_PSK)])
    # nodenum -> 32 byte X25519 private key
    private_keys: dict[int, bytes] = field(default_factory=dict)
    # nodenum -> 32 byte X25519 public key (learned from NodeInfo or given)
    public_keys: dict[int, bytes] = field(default_factory=dict)

    def learn(self, node: int, pub: bytes):
        if len(pub) == 32:
            self.public_keys[node] = pub


@dataclass
class Decoded:
    packet: "mesh_pb2.MeshPacket"
    raw: bytes
    crypto: str  # "none" | "channel" | "pki" | "undecryptable" | "plaintext"
    channel_name: str | None = None
    channel_key: bytes | None = None
    data: "mesh_pb2.Data | None" = None
    plaintext: bytes | None = None


def decode_datagram(raw: bytes, keys: Keyring) -> Decoded:
    p = mesh_pb2.MeshPacket()
    p.ParseFromString(raw)
    if p.WhichOneof("payload_variant") == "decoded":
        return Decoded(p, raw, "plaintext", data=p.decoded)
    if p.WhichOneof("payload_variant") != "encrypted":
        return Decoded(p, raw, "none")
    enc = p.encrypted
    # PKI: channel hash 0, unicast, and we hold the recipient's private key and sender's public key
    if p.channel == 0 and p.to != 0xFFFFFFFF and p.to in keys.private_keys and getattr(p, "from") in keys.public_keys:
        try:
            sk = pki_shared_key(keys.private_keys[p.to], keys.public_keys[getattr(p, "from")])
            pt = decrypt_pki(sk, p.id, getattr(p, "from"), enc)
            d = mesh_pb2.Data()
            d.ParseFromString(pt)
            return Decoded(p, raw, "pki", data=d, plaintext=pt)
        except (InvalidTag, ValueError, Exception):  # noqa: BLE001
            pass
    for name, key in keys.channels:
        if channel_hash(name, key) != p.channel:
            continue
        pt = decrypt_ctr(key, p.id, getattr(p, "from"), enc)
        d = mesh_pb2.Data()
        try:
            d.ParseFromString(pt)
        except Exception:  # noqa: BLE001
            continue
        if d.portnum == 0 and not d.payload:
            continue
        return Decoded(p, raw, "channel", channel_name=name, channel_key=key, data=d, plaintext=pt)
    return Decoded(p, raw, "undecryptable")


def node_id(n: int) -> str:
    return f"!{n:08x}"


def format_decoded(dec: Decoded, src: str) -> str:
    p = dec.packet
    frm = getattr(p, "from")
    hdr = {
        "src": src,
        "from": node_id(frm),
        "to": node_id(p.to),
        "id": f"0x{p.id:08x}",
        "ch": f"0x{p.channel:02x}",
        "hop_limit": p.hop_limit,
        "hop_start": p.hop_start,
        "want_ack": p.want_ack,
        "next_hop": f"0x{p.next_hop:02x}",
        "relay": f"0x{p.relay_node:02x}",
        "prio": mesh_pb2.MeshPacket.Priority.Name(p.priority) if p.priority else 0,
        "via_mqtt": p.via_mqtt,
        "transport": p.transport_mechanism,
        "rx_time": p.rx_time,
        "rx_snr": p.rx_snr,
        "rx_rssi": p.rx_rssi,
        "pki": p.pki_encrypted,
        "len": len(dec.raw),
        "crypto": dec.crypto,
    }
    out = json.dumps(hdr)
    if dec.data is not None:
        d = dec.data
        try:
            pn = portnums_pb2.PortNum.Name(d.portnum)
        except ValueError:
            pn = str(d.portnum)
        body = {
            "portnum": pn,
            "want_response": d.want_response,
            "dest": node_id(d.dest) if d.dest else 0,
            "source": node_id(d.source) if d.source else 0,
            "request_id": f"0x{d.request_id:08x}" if d.request_id else 0,
            "reply_id": d.reply_id,
            "emoji": d.emoji,
            "bitfield": d.bitfield if d.HasField("bitfield") else None,
            "payload": payload_to_dict(d.portnum, d.payload),
            "payload_hex": d.payload.hex(),
        }
        out += "\n    " + json.dumps(body)
    elif dec.crypto == "undecryptable":
        out += f"\n    encrypted={p.encrypted.hex()}"
    return out


def open_socket(groups, iface: str = "0.0.0.0", port: int = PORT) -> socket.socket:
    s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM, socket.IPPROTO_UDP)
    # meshtasticd binds <group>:4403 with SO_REUSEADDR; we must share the port
    s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    if hasattr(socket, "SO_REUSEPORT"):
        s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEPORT, 1)
    s.bind(("", port))
    for g in groups:
        mreq = socket.inet_aton(g) + socket.inet_aton(iface)
        s.setsockopt(socket.IPPROTO_IP, socket.IP_ADD_MEMBERSHIP, mreq)
    s.setsockopt(socket.IPPROTO_IP, socket.IP_MULTICAST_LOOP, 1)
    return s


def parse_keyring(args) -> Keyring:
    kr = Keyring()
    for spec in args.psk or []:
        name, _, b64 = spec.partition("=")
        kr.channels.append((name, expand_psk(base64.b64decode(b64))))
    for spec in args.privkey or []:
        n, _, k = spec.partition("=")
        num = int(n.lstrip("!"), 16)
        priv = bytes.fromhex(k) if len(k) == 64 else base64.b64decode(k)
        kr.private_keys[num] = priv
        kr.public_keys[num] = X25519PrivateKey.from_private_bytes(priv).public_key().public_bytes_raw()
    return kr


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--group", action="append", help="multicast group(s) (default: 239.0.0.69 and 224.0.0.69)")
    ap.add_argument("--iface", default="0.0.0.0", help="local interface IP to join on")
    ap.add_argument("--psk", action="append", help="extra channel NAME=base64psk (1-byte aliases allowed)")
    ap.add_argument("--privkey", action="append", help="NODEHEX=privkey (hex or base64) for PKI decryption")
    ap.add_argument("--jsonl", help="also append raw datagrams (hex) + metadata to this file")
    ap.add_argument("--count", type=int, default=0, help="exit after N datagrams")
    args = ap.parse_args()

    kr = parse_keyring(args)
    groups = args.group or list(GROUPS)
    s = open_socket(groups, args.iface)
    print(f"listening on {groups} :{PORT}", file=sys.stderr)
    jf = open(args.jsonl, "a") if args.jsonl else None
    n = 0
    while True:
        raw, addr = s.recvfrom(4096)
        n += 1
        try:
            dec = decode_datagram(raw, kr)
        except Exception as e:  # noqa: BLE001
            print(f"{addr[0]}: undecodable datagram ({e}): {raw.hex()}")
            continue
        if dec.data is not None and dec.data.portnum == portnums_pb2.NODEINFO_APP:
            u = mesh_pb2.User()
            try:
                u.ParseFromString(dec.data.payload)
                kr.learn(getattr(dec.packet, "from"), u.public_key)
            except Exception:  # noqa: BLE001
                pass
        print(time.strftime("%H:%M:%S"), format_decoded(dec, addr[0]), flush=True)
        if jf:
            jf.write(json.dumps({"t": time.time(), "src": addr[0], "raw_hex": raw.hex()}) + "\n")
            jf.flush()
        if args.count and n >= args.count:
            break


if __name__ == "__main__":
    main()
