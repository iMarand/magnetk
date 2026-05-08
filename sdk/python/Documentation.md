# Magnetk Python SDK Documentation

The Python SDK is a convenience wrapper around the native Go `seed` and `peer` binaries. It is ideal when you want to control Magnetk from Python scripts while still using the production transfer engine.

The Python SDK supports:

- sharing by spawning `seed`
- downloading by spawning `peer`
- relay identity lookup
- relay-assisted mode flags
- hole-punch mode flags
- automatic config file creation
- Magnetk link parsing through `MagnetkLink`

## Requirements

The native binaries must be available through `PATH`, environment variables, or config.

Environment variables:

```powershell
$env:MAGNETK_SEED_PATH="D:\More_Projects\Projects\Magnetk\bin\seed.exe"
$env:MAGNETK_PEER_PATH="D:\More_Projects\Projects\Magnetk\bin\peer.exe"
```

Or pass explicit paths:

```python
client = MagnetkClient({
    "seeder_path": r"D:\More_Projects\Projects\Magnetk\bin\seed.exe",
    "peer_path": r"D:\More_Projects\Projects\Magnetk\bin\peer.exe",
})
```

## Share A File

```python
from magnetk import MagnetkClient
import time

client = MagnetkClient({
    "relay_url": "69.169.109.243:4003",
    "seed_port": 4002,
    "hole_punch": True,
    "relay_assisted": "auto",
    "transport_mode": "auto",
})

link = client.share(r"D:\MUSIC\el.mp4")
print(link)

while True:
    time.sleep(1)
```

The Python SDK starts `seed` in the background. Keep your Python process alive while you want to serve the file.

Example:

```powershell
cd sdk\python
python examples\share.py
```

## Download A File

```python
from magnetk import MagnetkClient

client = MagnetkClient({
    "relay_url": "69.169.109.243:4003",
    "hole_punch": True,
    "relay_assisted": "auto",
    "transport_mode": "auto",
})

path = client.get("magnetk:?...", "./downloads/")
print("saved to", path)
```

Example:

```powershell
cd sdk\python
python examples\download.py "magnetk:?..."
```

## Configuration

```python
client = MagnetkClient({
    "relay_url": "69.169.109.243:4003",
    "relays": [],
    "seed_port": 4002,
    "peer_port": 0,

    "dht_enabled": False,
    "dht_bootstrap_peers": [],
    "discovery_priority": ["dht", "relay", "direct"],

    "stun_servers": ["stun:stun.l.google.com:19302"],
    "hole_punch": True,
    "relay_assisted": "auto",
    "enable_relay_fallback": True,
    "relay_timeout": 5000,
    "transport_mode": "auto",

    "seeder_path": None,
    "peer_path": None,
    "config_path": None,
    "auto_config": True,
})
```

Important fields:

- `relay_url`: relay TCP identity/discovery address, usually `host:4003`.
- `seed_port`: seed data port, usually `4002`.
- `relay_assisted`: `"auto"`, `"true"`, `"false"`, `True`, or `False`.
- `hole_punch`: passes `-holepunch` to the native binaries.
- `transport_mode`: `"auto"`, `"quic"`, or `"tcp"`.
- `seeder_path`, `peer_path`: explicit binary locations.

## Method Reference

### `MagnetkClient(config=None)`

Creates a client wrapper.

### `client.share(file_path)` / `client.seed(file_path)`

Starts the native `seed` binary and returns the generated magnet link.

### `client.get(magnet_uri, output_path=None)` / `client.download(magnet_uri, output_path)`

Starts the native `peer` binary and waits for completion.

### `client.hash(file_path)`

Calculates SHA-256 of a local file.

### `MagnetkLink.parse(uri)`

Parses a `magnetk:?` link.

## Relay Address Notes

The Python SDK expects `relay_url` like:

```text
69.169.109.243:4003
```

It connects to `4003` to fetch the relay Peer ID, then passes a full multiaddr to the native seed:

```text
/ip4/69.169.109.243/tcp/4001/p2p/<RELAY_PEER_ID>
```

## Relay-Assisted Behavior

Because Python uses the native `peer`, transfer behavior matches the CLI:

- direct MsQuic/TCP first
- libp2p circuit relay fallback when allowed
- hole-punch attempt when possible

Use `"relay_assisted": "false"` only when you require real direct P2P and want failure instead of relay data.

## Verify

```powershell
cd sdk\python
python -m py_compile magnetk\client.py magnetk\link.py
```
