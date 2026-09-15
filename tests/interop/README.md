# Interop harness: real meshtasticd over UDP multicast

This harness runs real Meshtastic firmware (the Linux/Portduino `meshtasticd`, official
Docker image) with no radio. The instances mesh with each other over **UDP multicast**,
so RepeaterTastic can join the same mesh as a peer. The harness also produces the golden
test vectors in `vectors/` that the Go unit tests use.

Python here is reference tooling only (the `meshtastic` client library, a sniffer and the
capture scripts). None of it is product code.

## Firmware versions

| image tag (pinned) | firmware | multicast group | node number |
|---|---|---|---|
| `meshtastic/meshtasticd:2.7.26.54e0d8d-debian` (default; `latest`/`beta` on Docker Hub) | 2.7.26.54e0d8d | **224.0.0.69**:4403 | MAC bytes 2..5 (from `-h`) |
| `meshtastic/meshtasticd:2.8.0.47db0e3-alpha-debian` (newest 2.8, alpha only) | 2.8.0.47db0e3 | **239.0.0.69**:4403 | **crc32(public_key)** once keys exist |

The multicast group changed between 2.7 and 2.8 (`UdpMulticastHandler.h`), so a 2.7
node and a 2.8 node on the same LAN **never hear each other**. To talk to both,
RepeaterTastic has to join and send on both groups. There is no stable 2.8 image yet
(2.8.1 exists only as `daily`/`GHA-*` tags). Pick the image with `MESHTASTICD_IMAGE=...`.

## Quick start

```bash
cd tests/interop
~/.local/bin/uv venv .venv && ~/.local/bin/uv pip install --python .venv/bin/python meshtastic cryptography protobuf

./run_meshtasticd.sh up 2                 # repeatertastic-mtd-1 (API 127.0.0.1:4410), -2 (4411)
.venv/bin/python setup_nodes.py 127.0.0.1:4410 127.0.0.1:4411 --lat 51.5 --lon -0.12
.venv/bin/python setup_nodes.py 127.0.0.1:4410 127.0.0.1:4411 --show-nodes   # idempotent; shows discovery

./run_meshtasticd.sh tools udp_sniff.py   # live decode of the multicast traffic (inside the docker net)
./run_meshtasticd.sh tools -m meshtastic --host repeatertastic-mtd-1:4410 --sendtext hi

# golden vectors (about 2.5 min). Best from fresh state, see "NodeInfo throttling" below.
./run_meshtasticd.sh down --wipe && ./run_meshtasticd.sh up 2
.venv/bin/python setup_nodes.py 127.0.0.1:4410 127.0.0.1:4411 --lat 51.5 --lon -0.12 && sleep 10
./run_meshtasticd.sh tools capture_vectors.py repeatertastic-mtd-1:4410 repeatertastic-mtd-2:4411 \
    --image "$MESHTASTICD_IMAGE"
.venv/bin/python verify_vectors.py        # re-checks every vector using only its hex fields

./run_meshtasticd.sh down [--wipe]        # stop/remove containers (+ delete state/)
```

Files:

- `run_meshtasticd.sh` has the subcommands `up N`, `down [--wipe]`, `status`, `logs I [-f]`,
  `tools ARGS` and `build-tools`. `tools ARGS` runs `python ARGS` in `repeatertastic-interop-tools:1`
  (built from `tools/Dockerfile`) on the mesh network, with this directory mounted.
- `setup_nodes.py` configures each node idempotently: EU_868, LongFast preset, hop limit 3,
  `UDP_BROADCAST`, default channel, owner name, and an optional fixed position.
- `udp_sniff.py` is a standalone sniffer. It joins both groups, decrypts AES-CTR channel
  traffic with the LongFast key (and any `--psk NAME=b64`), and decrypts PKI DMs with
  `--privkey NODEHEX=hex`. Its functions are reused by `capture_vectors.py`.
- `capture_vectors.py` drives traffic over the TCP API, records multicast traffic and
  writes `vectors/<fw>/*.json`, `index.json` (node keys and notes) and `capture.jsonl`
  (every datagram).
- `verify_vectors.py` checks each vector: packet parse, CTR/CCM decrypt, X25519/SHA-256
  key, and Data parse. Go tests should run the same checks.

## How meshtasticd runs without a radio

- Each instance gets `config.yaml` with `Lora: Module: sim`, which selects SimRadio.
  Do not use `--sim`/`-s`. That flag sets `portduino_config.force_simradio`, which
  (a) skips loading config.yaml entirely and (b) disables PKI in
  `Router::perhapsEncode`. With `Module: sim` (or with no config.yaml at all), PKI works.
- `Config: EnableUDP: true` in the YAML forces `network.enabled_protocols = UDP_BROADCAST`.
  `setup_nodes.py` also sets it over the API. On Portduino the UDP handler starts at boot,
  and only if that bit is set.
- Options used: `-h 02001EE7A0II` (hwid, gives node `!1ee7a0II` on 2.7), `-p 44xx` (TCP API port),
  `-d /data/vfs` (persistent VFS per instance under `state/nodeI/`), and `--user uid:gid` so
  the state files are yours.
- Start the daemon as **`/usr/bin/meshtasticd`** (absolute path). Setting the owner name,
  and some other admin actions, make the firmware "reboot" by `execv(argv[0])`. With a bare
  `meshtasticd` that fails ("execv() returned -1") and the container exits. The script also
  sets `--restart unless-stopped`.
- The firmware only generates its X25519 key pair once a region is set. Until then it has
  no PKI keys and DMs fail with `NO_CHANNEL`.
- If the VFS is not writable (for example left over from a root-run container), the
  firmware loops on "Can't write prefs" / "critical error 13".

## Networking: bridge (default) vs host

The task called for `--network host`. **On this workstation host-mode multicast cannot
work**, because ufw is active with `INPUT DROP` and no rule for UDP 4403. A multicast
datagram looped back to the host arrives on `enp5s0` (not `lo`) and gets dropped, even
between two processes on the same host. A plain Python send/receive test on the host
fails too.

- **`NET_MODE=bridge` (default, verified):** a user-defined docker bridge
  `repeatertastic-mesh`. `br_netfilter` is not loaded, so bridged multicast between
  containers is not filtered. Each node has its own IP (172.x). The TCP APIs are published
  on `127.0.0.1:44xx`. Anything that needs multicast (sniffer, capture, or RepeaterTastic
  itself) must run in a container on that network (`./run_meshtasticd.sh tools ...`, or
  `docker run --network repeatertastic-mesh ...`).
- **`NET_MODE=host`:** needs a firewall rule, for example
  `sudo ufw allow in proto udp to 224.0.0.69 port 4403` and the same for `239.0.0.69`
  (or allow from your LAN). The kernel joins the group on the default-route interface
  (`INADDR_ANY`), so a host without a default route, or with several NICs, may pick the wrong
  one. Loopback delivery to other processes on the same host needs `IP_MULTICAST_LOOP=1`,
  which is the default and is set explicitly in `udp_sniff.py`. Receivers must set
  `SO_REUSEADDR` (and `SO_REUSEPORT`) because every meshtasticd binds `<group>:4403`.
  Host mode is implemented in the script but was not verified here.

## MeshPacket-over-UDP findings (verified on 2.7.26 and 2.8.0)

- Each datagram is one bare protobuf `meshtastic.MeshPacket`, with no framing and no
  16-byte LoRa header. The firmware sends the in-memory packet at `Router::send`, so every
  header field is carried as a protobuf field: `from`, `to`, `id`, `channel`, `hop_limit`,
  `hop_start`, `want_ack`, `next_hop`, `relay_node`, `priority`, and `pki_encrypted` on
  PKI sends.
- **`encrypted` is always populated.** Receivers only accept the `encrypted` variant and
  silently drop `decoded`. They also drop `from == 0` or `from == own node`. Since 2.8 they
  also drop `hop_limit`/`hop_start > 7`, and they clear `pki_encrypted`/`public_key` on
  ingress.
- `channel` holds the **8-bit channel hash** (LongFast default = `0x08`) for channel traffic,
  and `0` for PKI DMs.
- `rx_snr`, `rx_rssi` and `rx_time` are not set in anything we captured. The receiver zeroes
  SNR/RSSI on UDP ingress. A node relaying a *LoRa*-received packet would presumably leak its
  RX metadata, because the whole struct is encoded; that path could not be tested without
  hardware.
- **Re-broadcast:** a node that hears a broadcast over UDP floods it back onto UDP. The copy
  has `hop_limit-1`, `relay_node` = the relayer's last byte, and
  `transport_mechanism = 6` (`TRANSPORT_MULTICAST_UDP`) left set in the protobuf. The
  firmware logs "Attempt to send UDP sourced packet over UDP" and sends it anyway. The
  original sender drops the echo as a duplicate. DMs addressed to the receiver are not
  re-flooded.
- **PKI works.** DMs and telemetry request/reply are PKI (`channel = 0`,
  `pki_encrypted = true`, `encrypted = ciphertext | tag(8) | extra_nonce(4)`). NodeInfo,
  Position, Routing and Traceroute are never PKI. ACKs use the channel key.
- ACKs: B's ACK for the DM is a ROUTING_APP with `request_id = DM id`, `hop_limit = hop_start = 2`,
  `want_ack = true` and `priority = ACK`. A then answers with a **zero-hop** ACK
  (`hop_limit = hop_start = 0`). After the first exchange, `next_hop` gets filled with the
  peer's last byte.
- Data protobufs from the firmware always carry `bitfield` (value 0, encoded as `48 00`).
- 2.7 default channel `position_precision` is 13, so the position reply has `precision_bits: 13`.
  In 2.8 the default is 0, so a position request gets a `NO_RESPONSE` NAK (`routing_nak.json`).

## Crypto (as implemented by the firmware, and checked against these vectors)

- Channel: AES-CTR (AES-128 for the 16-byte default key). IV = `id u64 LE | from u32 LE | 0 u32`,
  and the whole 16-byte block is the counter. The key is `d4f1bb3a20290759f0bcffabcf4e6901`
  (PSK alias `0x01`). Channel hash = `xor(name bytes) ^ xor(key bytes)`, where the name is
  the preset name ("LongFast") when the channel name is empty.
- PKI: `key = SHA256(X25519(my_priv, peer_pub))`, AES-256-CCM with an 8-byte tag and L=2.
  The 13-byte nonce is `id u32 LE | extra_nonce u32 LE | from u32 LE | 0x00`.
  `extra_nonce` is random and appended after the tag.

## Gotchas

- **NodeInfo throttling:** the firmware sends NodeInfo at most every 600 s (60 s for a
  "nodeinfo ping", which is `ToRadio.heartbeat.nonce = 1`). The send history is persisted
  in the VFS and survives restarts. `capture_vectors.py` retries pings until both nodes
  know each other's key. `nodeinfo_reply` is best effort (captured on 2.8, missed on 2.7).
- Changing the owner triggers a firmware reboot after about 7 s, so `setup_nodes.py` must
  not be interrupted halfway. Re-running it is safe.
- With 2.8 the `-h` hwid only seeds the node number before the key pair exists. After key
  generation the node renumbers to `crc32(public_key)` (seen after the setup reboot).
- The `meshtastic` Python library prints a `BrokenPipe` traceback while connecting, then
  reconnects. It is harmless.
- Don't reuse `vfs` dirs between 2.7 and 2.8 images; use `down --wipe`.

## Vectors

`vectors/2.7.26.54e0d8d/` and `vectors/2.8.0.47db0e3/`: `nodeinfo_broadcast`,
`nodeinfo_reply` (2.8), `text_broadcast`, `text_broadcast_relayed`, `dm_text` (PKI),
`routing_ack`, `routing_ack_zero_hop`, `position_request`, `position_reply` (2.7),
`routing_nak` (2.8), `telemetry_request` (PKI), `telemetry_reply` (PKI),
`traceroute_request` and `traceroute_reply`, plus `index.json` and `capture.jsonl`.

Each vector has these keys:

- `udp.datagram_hex`: the full UDP payload. Also `udp.dst_group`.
- Header fields: `from`, `to`, `id`, `channel_hash`, `hop_limit`, `hop_start`, `want_ack`,
  `next_hop`, `relay_node`, `priority`, `transport_mechanism`, `rx_*`, `pki_encrypted`, and
  `all_set_fields` (which protobuf fields are present).
- `encrypted_hex` and `plaintext_hex` (the Data protobuf).
- `derived_radio_header_hex`: the 16-byte on-air header computed from the fields. It is not
  on the UDP wire.
- `crypto`: either the channel key, name, hash and IV, or for PKI both nodes' private and
  public keys, the X25519 secret, AES key, nonce, extra nonce, ciphertext and tag.
- `decoded`: `portnum`, `payload_hex`, `request_id`, `want_response`, `bitfield`, and so on,
  plus a parsed `payload` for convenience.
