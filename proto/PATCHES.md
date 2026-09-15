# Local patches to vendored protobufs

Upstream: meshtastic/protobufs @ 723a31e42013f155b529929e675db8caef20c534

- `meshtastic/admin.proto`: message `AS3935_config` renamed to `AS3935AdminConfig`. Go camel-cases it to the same identifier as `AS3935Config` in telemetry.proto. Message names are not on the wire, so this is wire-compatible.
