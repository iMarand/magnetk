# Magnetk JavaScript SDK Documentation

The JavaScript SDK is a Node.js SDK for Magnetk apps. It has two roles:

- Share files by spawning the native Go `seed` binary.
- Download files in pure JavaScript over direct TCP or the relay TCP proxy.

Important limitation: the JS SDK does not use native MsQuic or native libp2p DCUtR directly. For full native direct MsQuic and hole punching behavior, use the Go SDK or CLI. The JS SDK still works across networks through the relay TCP proxy when direct TCP is unreachable.

## Install

From this repository:

```powershell
cd sdk\js\magnetk
npm install
npm test
```

Import:

```js
import { MagnetkClient, MagnetkLink, CONNECTION_TYPE } from './index.js';
```

If installed as a package:

```js
import { MagnetkClient } from 'magnetk';
```

## Requirements

For sharing, the SDK must find the native `seed` binary.

Set one of:

```powershell
$env:MAGNETK_SEEDER_PATH="D:\More_Projects\Projects\Magnetk\bin\seed.exe"
```

or pass:

```js
const client = new MagnetkClient({
  seederPath: 'D:\\More_Projects\\Projects\\Magnetk\\bin\\seed.exe'
});
```

For pure JS downloads, no `peer` binary is required.

## Share A File

```js
import { MagnetkClient } from './index.js';

const client = new MagnetkClient({
  relayUrl: '69.169.109.243',
  relayPort: 4003,
  seedPort: 4002,
  holePunch: true,
  relayAssisted: 'auto',
  transportMode: 'auto'
});

client.on('hashing', p => console.log(`hashing ${p.percent}%`));
client.on('relay-connected', r => console.log(`relay ${r.host}`));
client.on('seeding', info => console.log(`seeding ${info.fileName}`));

const link = await client.share('./app.txt');
console.log(link);

await client.keepSeeding();
```

Example script:

```powershell
cd sdk\js\magnetk
node share.js ./app.txt
```

## Download A File

```js
import { MagnetkClient, CONNECTION_TYPE } from './index.js';

const client = new MagnetkClient({
  relayUrl: '69.169.109.243',
  relayPort: 4003,
  seedPort: 4002,
  relayAssisted: 'auto'
});

client.on('connection-type', c => {
  console.log(c.type); // DIRECT, LOCAL, RELAYED, UNKNOWN
});

client.on('progress', p => {
  console.log(`${p.percent.toFixed(1)}% ${p.speed} KB/s`);
});

client.on('complete', info => {
  console.log(`saved to ${info.filePath}`);
});

await client.get('magnetk:?...', './downloads');
```

Example script:

```powershell
cd sdk\js\magnetk
node download.js "magnetk:?..." ./downloads
```

## Configuration

```js
const client = new MagnetkClient({
  relayUrl: '69.169.109.243',
  relayPort: 4003,
  relays: [],
  seedPort: 4002,
  seederPath: null,
  configPath: null,

  dhtEnabled: false,
  dhtBootstrapPeers: [],
  discoveryPriority: ['dht', 'relay', 'direct'],

  stunServers: ['stun:stun.l.google.com:19302'],
  holePunch: true,
  relayAssisted: 'auto',
  enableRelayFallback: true,
  relayTimeout: 5000,
  transportMode: 'auto',

  autoConfig: true
});
```

Important fields:

- `relayUrl`: relay host, for example `69.169.109.243`.
- `relayPort`: relay TCP discovery fallback port, usually `4003`.
- `seedPort`: data port used by the native seed, usually `4002`.
- `relayAssisted`: `"auto"`, `"true"`, `"false"`, or boolean.
- `enableRelayFallback`: permits the relay TCP proxy path.
- `seederPath`: explicit path to `seed` or `seed.exe`.

## Events

### Sharing

- `validating-relay`
- `hashing`: `{ percent }`
- `relay-connected`: `{ host, port }`
- `seeding`: `{ fileName, fileSize, hash }`
- `error`: `{ code, message }`

### Downloading

- `download-start`: `{ fileName }`
- `connection-type`: `{ type }`
- `progress`: `{ percent, bytesDownloaded, totalBytes, speed, chunkIndex, chunkBytes, chunkTotalBytes }`
- `complete`: `{ filePath }`
- `error`: `{ code, message }`

## Method Reference

### `new MagnetkClient(config)`

Creates a client/seeder wrapper.

### `client.share(filePath)` / `client.seed(filePath)`

Hashes the file, starts `seed`, waits for the magnet link, then returns it.

### `client.keepSeeding()`

Keeps the Node process alive while the spawned seed process serves data.

### `client.stop()`

Stops spawned seed processes.

### `client.get(magnetURI, outputPath)` / `client.download(magnetURI, outputPath)`

Downloads the file to a directory or exact file path. The SDK verifies SHA-256 after download.

### `client.lookupSeeds(fileHash)`

Queries relay discovery and returns seed entries.

### `client.hash(filePath)`

Returns SHA-256 for a local file.

### `MagnetkLink.parse(uri)`

Parses a `magnetk:?` URI.

## Relay-Assisted Behavior

Pure JS download order is:

1. Direct TCP addresses from relay lookup.
2. Local TCP fallback.
3. Relay TCP proxy if `relayAssisted !== "false"` and `enableRelayFallback !== false`.

On mobile hotspot or carrier NAT, direct TCP often fails. Relay proxy is expected there.

## Verify

```powershell
cd sdk\js\magnetk
npm test
npm pack --dry-run
```
