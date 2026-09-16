# Several radios

[← README](../README.md) · [Hardware](hardware.md) · [Configuration](configuration.md) · [Web GUI](web-gui.md) · [MQTT](mqtt.md) · [HTTP API](api.md) · [Architecture](architecture.md)

Add radios under `radios:` in the config: each gets its own modem, preset, relay persona,
identities, node DB and airtime budget, so one host can serve LongFast and MediumFast side by side.
The top-level radio stays the **main** radio and existing configs keep working unchanged.

- **Overlapping channels:** radios whose channels overlap in frequency take turns to transmit. In
  EU_868, LongFast, MediumFast, MediumSlow and ShortFast all sit on 869.525 MHz.
- **Site airtime cap:** `site.duty_cycle_percent` caps the summed airtime of all radios.
- **Web GUI:** Configuration → Radios adds, edits, renames and removes radios, and sets the site
  airtime cap. A radio switcher appears in the top bar once there's more than one; the Relay,
  Airtime, Position and MQTT tabs and most pages follow it. A new radio starts at the next restart
  (its settings can still be edited before then).
- **Identities:** each lives on one radio. Move one from its editor: key, node ID, app port and
  chats go with it, and its primary channel follows the new preset. A key can't be imported onto a
  second radio.
- **Relay:** each radio has its own relay role: a Meshtastic role (client, client_base, client_mute, router,
  router_late), monitor (listens only) or off
  (the radio is ignored). Set it from the top bar or Configuration → Relay with that radio picked.
- **API:** `?radio=<id>` selects a radio (see [HTTP API](api.md#radios-and-the-radio-parameter)).
- **EU_868 notes:** LongTurbo's 500 kHz doesn't fit the 250 kHz sub-band, and LongSlow sits on 869.4625 MHz.

## Adding a radio

1. Flash and plug in another board ([Hardware](hardware.md)); note its `/dev/serial/by-id/` path.
2. **Configuration → Radios → Add radio:** pick the region, preset, device, TX power and relay role
   (a Meshtastic role, monitor or off). The relay starts as client mute so a new radio doesn't repeat until you decide
   it should.
3. Restart when the banner asks. The radio comes up with its own relay persona.
4. Switch to it in the top bar to add identities, MQTT connections and a position.

Or in the config:

```yaml
radios:
    - id: mf
      name: MediumFast
      radio: {driver: kiss, device: /dev/serial/by-id/usb-…-if00, baud: 115200}
      mesh: {preset: MEDIUM_FAST, tx_power_dbm: 20, hop_limit: 3}
      relay: {role: client_mute}
site:
    duty_cycle_percent: 10
```

Extra radios keep their identities and history in `state_dir/radios/<id>`.

Each identity lives on one radio; there's no routing an identity's channels or DMs across several.
Radios on one mast still join into a site (`mesh.JoinSite`): an MQTT link recognises the site's own
identities coming back from a broker on any radio, and `GET /nodes/{id}/sightings`
([HTTP API](api.md#nodes)) shows what every radio of the site has heard of a node.
