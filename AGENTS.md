# AGENTS.md

Guidance for coding agents (and humans) working on RepeaterTastic. Read this before changing code.

## What this is

A Go daemon that hosts many virtual Meshtastic nodes ("identities") on one or more Mesh KISS LoRa
modems, with a Vue 3 web GUI embedded in the binary. See `README.md` for the feature tour and
`docs/api.md` for the HTTP API.

## Commands

```bash
go vet ./... && go test ./...        # must pass before every commit
go test -race ./...                  # for changes to internal/mesh, internal/site, internal/links
cd ui && npm ci && npm run build     # vue-tsc + vite; writes internal/web/dist (commit it)
make build | make dist               # binaries; MAP_API_KEY=... bakes in the map tile key
docker build -t repeatertastic .     # container image
RT_TEST_MQTT_BROKER=host:1883 go test ./internal/links/mqtt   # MQTT against a real broker
```

`internal/web/dist` is committed: any change under `ui/` needs `npm run build` and the rebuilt
`dist` in the same commit, or the daemon serves the old GUI.

## Layout

- `cmd/repeatertastic` daemon entry point (flags, env overrides, radio start-up, federation).
- `internal/mesh` the stack: receive (dedupe, decrypt, deliver), send (queue, retries, ACKs),
  relay, identities, node DB, airtime, experimental multi-radio federation (`federation.go`).
- `internal/phoneapi` the Meshtastic client API each identity serves (TCP stream + HTTP).
- `internal/web` REST/SSE API and auth; `server.go` registers routes (`pub`, `setup`, `priv`).
- `internal/config` YAML config, defaults, validation, `ApplyEnv`.
- `internal/links/mqtt`, `internal/links/udp`, `internal/site` (several radios on one host).
- `ui/src` GUI: `views/` pages, `components/` (identities, config, packets, nodes, layout, ui),
  `store/live.ts` shared live state, `api/types.ts` API types (keep in step with the Go JSON).

## Rules that matter

- **Airtime is shared with real people.** Never add traffic that repeats on a timer without a
  duty-cycle check (`dutyLimit`, channel utilisation) and a sensible minimum interval. Only the
  relay persona repeats; new identities default to `CLIENT_MUTE`.
- **One way to do each thing.** The GUI has one dialog per job (for example
  `ChannelSlotDialog.vue` for every channel slot add/edit). Extend it; don't add a second path.
- **Controls must save.** Every GUI control maps to a config or API field that round-trips; test it
  in `internal/web/*_test.go`.
- **Multi-radio model (experimental, `experimental.multi_radio_identities`):** every channel slot is
  on exactly one radio; each identity has a default radio (slot 0 is its primary; new or changed
  slots use it); DMs use best-heard/default/fixed. A different channel put in a slot resets its radio.
  With the switch off everything behaves as a single radio. Keep these rules in `federation.go`,
  the API validation (`handlers.go`) and the GUI in agreement.
- **Secrets:** never log or print MQTT passwords, private keys, API tokens or the map API key.
  The map key reaches builds only via `-X main.mapAPIKey` / a BuildKit secret, and at run time via
  `REPEATERTASTIC_MAP_API_KEY`. JSON uses `json:"-"` for passwords; keep it that way.
- **Config files** are written by yaml.v3 with 4-space indentation; don't hand-edit them with
  2-space inserts. Validation lives in `config.Validate`; add new fields there and to
  `deploy/repeatertastic.example.yaml`.
- **Locks:** don't call into another host (radio) while holding a host's `mu`/`chanMu`; the
  federation reads identities across hosts.
- **Protobufs** in `internal/pb` are generated (`scripts/gen-proto.sh`); don't edit by hand.

## Style

- Go: small functions, comments that say why, errors that tell the user what to do
  ("slot 0 is the default radio's primary channel; change the identity's default radio instead").
- GUI copy: plain words from the user's side ("Restart to apply", not "restart_required"),
  sentence case, no jargon without an explanation.
- Tests: table-driven where it helps; simulated radios (`internal/radio/sim`) for mesh behaviour.
- Commits: imperative subject, a body that explains the change and why.

## Before you finish

1. `go vet ./... && go test ./...` pass; `npm run build` passes if `ui/` changed, and `dist` is committed.
2. README, `docs/api.md` and the example config match the change.
3. No secrets in code, logs, tests or commit messages.
