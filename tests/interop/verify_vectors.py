#!/usr/bin/env python3
"""Self-check golden vectors using only the hex fields inside each JSON file.

For every vectors/*/*.json (except index.json) it checks:
  * datagram_hex parses as MeshPacket and matches the recorded header fields / encrypted_hex
  * channel vectors: AES-CTR(channel_key, iv) over encrypted_hex == plaintext_hex, and
    channel_hash == xor(name) ^ xor(key)
  * PKI vectors: SHA256(X25519(receiver_priv, sender_pub)) == aes_key_hex and
    AES-CCM decrypt(ccm_nonce, ciphertext|tag) == plaintext_hex; X25519 is also checked
    in the other direction (sender_priv, receiver_pub)
  * plaintext_hex parses as Data with the recorded portnum / payload_hex / request_id
This is the same set of checks a Go unit test should perform.
"""

from __future__ import annotations

import hashlib
import json
import sys
from pathlib import Path

from cryptography.hazmat.primitives.asymmetric.x25519 import X25519PrivateKey, X25519PublicKey
from cryptography.hazmat.primitives.ciphers import Cipher, algorithms, modes
from cryptography.hazmat.primitives.ciphers.aead import AESCCM
from meshtastic.protobuf import mesh_pb2

HERE = Path(__file__).resolve().parent


def xor(b: bytes) -> int:
    h = 0
    for x in b:
        h ^= x
    return h


def check(path: Path) -> list[str]:
    v = json.loads(path.read_text())
    errs = []
    p = mesh_pb2.MeshPacket()
    p.ParseFromString(bytes.fromhex(v["udp"]["datagram_hex"]))
    for k, got in [("from", getattr(p, "from")), ("to", p.to), ("id", p.id), ("channel_hash", p.channel),
                   ("hop_limit", p.hop_limit), ("hop_start", p.hop_start), ("want_ack", p.want_ack),
                   ("next_hop", p.next_hop), ("relay_node", p.relay_node), ("encrypted_hex", p.encrypted.hex())]:
        if v[k] != got:
            errs.append(f"{k}: json={v[k]!r} datagram={got!r}")
    enc = bytes.fromhex(v["encrypted_hex"])
    c = v["crypto"]
    if c["type"] == "channel_aes_ctr":
        key = bytes.fromhex(c["channel_key_hex"])
        if xor(c["channel_name"].encode()) ^ xor(key) != v["channel_hash"]:
            errs.append("channel hash mismatch")
        d = Cipher(algorithms.AES(key), modes.CTR(bytes.fromhex(c["iv_hex"]))).decryptor()
        pt = d.update(enc) + d.finalize()
    elif c["type"] == "pki_x25519_aes_ccm":
        rpriv, spub = bytes.fromhex(c["receiver_private_key_hex"]), bytes.fromhex(c["sender_public_key_hex"])
        spriv, rpub = bytes.fromhex(c["sender_private_key_hex"]), bytes.fromhex(c["receiver_public_key_hex"])
        s1 = X25519PrivateKey.from_private_bytes(rpriv).exchange(X25519PublicKey.from_public_bytes(spub))
        s2 = X25519PrivateKey.from_private_bytes(spriv).exchange(X25519PublicKey.from_public_bytes(rpub))
        if s1 != s2 or s1.hex() != c["x25519_shared_secret_hex"]:
            errs.append("x25519 shared secret mismatch")
        if hashlib.sha256(s1).hexdigest() != c["aes_key_hex"]:
            errs.append("aes key mismatch")
        if enc[:-12].hex() != c["ciphertext_hex"] or enc[-12:-4].hex() != c["tag_hex"] or enc[-4:].hex() != c["extra_nonce_hex"]:
            errs.append("ciphertext/tag/extra_nonce split mismatch")
        pt = AESCCM(bytes.fromhex(c["aes_key_hex"]), tag_length=8).decrypt(
            bytes.fromhex(c["ccm_nonce_hex"]), bytes.fromhex(c["ciphertext_hex"] + c["tag_hex"]), None)
    else:
        return [f"unsupported crypto {c['type']}"]
    if pt.hex() != v["plaintext_hex"]:
        errs.append("plaintext mismatch")
    d = mesh_pb2.Data()
    d.ParseFromString(pt)
    dj = v["decoded"]
    if d.portnum != dj["portnum"] or d.payload.hex() != dj["payload_hex"] or d.request_id != dj["request_id"]:
        errs.append("decoded Data mismatch")
    return errs


def main():
    files = sorted(f for f in (HERE / "vectors").glob("*/*.json") if f.name != "index.json")
    bad = 0
    for f in files:
        errs = check(f)
        print(f"{'OK  ' if not errs else 'FAIL'} {f.relative_to(HERE)}" + ("" if not errs else f"  {errs}"))
        bad += bool(errs)
    print(f"{len(files) - bad}/{len(files)} vectors verified")
    return 1 if bad or not files else 0


if __name__ == "__main__":
    sys.exit(main())
