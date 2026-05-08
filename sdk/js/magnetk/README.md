# Magnetk JavaScript SDK

Node.js SDK for using Magnetk from JavaScript apps.

The JS SDK can:

- parse and generate `magnetk:?` links
- share files by spawning the native Go `seed` binary
- download files in pure JavaScript over direct TCP
- fall back to the relay TCP proxy when direct TCP is unreachable
- emit progress, connection type, and completion events
- verify the downloaded file hash

Pure JavaScript downloads do not use native MsQuic or native libp2p DCUtR hole punching directly. For the full native transport stack, use the Go CLI or Go SDK. The JS SDK is still useful across networks because it can use the relay TCP proxy.

For the complete reference, see [Documentation.md](./Documentation.md).

## Installation

From npm:

```bash
npm install magnetk
```

From this repository:

```powershell
cd sdk\js\magnetk
npm install
npm test
```

## Import

```js
import { MagnetkClient, MagnetkLink, CONNECTION_TYPE } from 'magnetk';
```

For local examples inside this folder:

```js
import { MagnetkClient } from './index.js';
```

## Requirements

Sharing requires the native Magnetk `seed` binary. Downloading can run in pure JS.

Make sure `seed` or `seed.exe` is in `PATH`, or set:

```powershell
$env:MAGNETK_SEEDER_PATH="D:\More_Projects\Projects\Magnetk\bin\seed.exe"
```

You can also pass it in code:

```js
const client = new MagnetkClient({
  seederPath: 'D:\\More_Projects\\Projects\\Magnetk\\bin\\seed.exe'
});
```

## Share A File

```js
import { MagnetkClient } from 'magnetk';

const client = new MagnetkClient({
  relayUrl: '69.169.109.243',
  relayPort: 4003,
  seedPort: 4002,
  holePunch: true,
  relayAssisted: 'auto',
  transportMode: 'auto'
});

client.on('hashing', p => console.log(`hashing ${p.percent}%`));
client.on('seeding', info => console.log(`seeding ${info.fileName}`));
client.on('relay-connected', r => console.log(`relay connected: ${r.host}`));

const link = await client.share('./my-file.zip');
console.log('Share this link:', link);

await client.keepSeeding();
```

Run the included example:

```powershell
cd sdk\js\magnetk
node share.js ./app.txt
```

## Download A File

```js
import { MagnetkClient, CONNECTION_TYPE } from 'magnetk';

const client = new MagnetkClient({
  relayUrl: '69.169.109.243',
  relayPort: 4003,
  seedPort: 4002,
  relayAssisted: 'auto'
});

client.on('connection-type', c => {
  if (c.type === CONNECTION_TYPE.DIRECT) console.log('direct TCP');
  if (c.type === CONNECTION_TYPE.LOCAL) console.log('local TCP');
  if (c.type === CONNECTION_TYPE.RELAYED) console.log('relay proxy');
});

client.on('progress', p => {
  process.stdout.write(
    `\r${p.percent.toFixed(1)}% ${p.bytesDownloaded}/${p.totalBytes} bytes ${p.speed} KB/s`
  );
});

client.on('complete', info => {
  console.log(`\nsaved to ${info.filePath}`);
});

await client.get('magnetk:?...', './downloads');
```

Run the included example:

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

Common settings:

- `relayUrl`: relay host, for example `69.169.109.243`
- `relayPort`: relay TCP discovery fallback port, usually `4003`
- `seedPort`: native seed data port, usually `4002`
- `relayAssisted`: `"auto"`, `"true"`, `"false"`, or boolean
- `enableRelayFallback`: allows the relay TCP proxy path
- `seederPath`: explicit path to `seed` or `seed.exe`

## Relay-Assisted Behavior

Download order:

1. Direct TCP addresses returned by relay discovery.
2. Local TCP fallback.
3. Relay TCP proxy if allowed.

Set `relayAssisted: "false"` when you want direct-only behavior. On mobile hotspots or carrier NAT, direct connections often fail, so use `"auto"` for practical cross-network downloads.

## Events

Sharing:

- `validating-relay`
- `hashing`: `{ percent }`
- `relay-connected`: `{ host, port }`
- `seeding`: `{ fileName, fileSize, hash }`
- `error`: `{ code, message }`

Downloading:

- `download-start`: `{ fileName }`
- `connection-type`: `{ type }`
- `progress`: `{ percent, bytesDownloaded, totalBytes, speed, chunkIndex, chunkBytes, chunkTotalBytes }`
- `complete`: `{ filePath }`
- `error`: `{ code, message }`

## API Summary

- `new MagnetkClient(config)`
- `client.share(filePath)` / `client.seed(filePath)`
- `client.keepSeeding()`
- `client.stop()`
- `client.get(magnetURI, outputPath)` / `client.download(magnetURI, outputPath)`
- `client.lookupSeeds(fileHash)`
- `client.hash(filePath)`
- `MagnetkLink.parse(uri)`
- `link.toString()`

## CLI

The package includes a small CLI:

```bash
magnetk lookup <sha256_hash> --relay 69.169.109.243
magnetk download "magnetk:?..." --output ./file.zip
```

## Troubleshooting

### `spawn seed.exe ENOENT`

The SDK cannot find the native seeder.

Fix it by:

- putting `seed.exe` in `PATH`
- setting `MAGNETK_SEEDER_PATH`
- passing `seederPath` in `MagnetkClient`

### Direct works on same Wi-Fi but not across networks

That is normal when NAT or hotspot routing blocks inbound connections. Use:

```js
relayAssisted: 'auto'
```

The SDK will fall back to the relay TCP proxy.

### Need full native MsQuic/libp2p behavior

Use the Go CLI or Go SDK. The JS SDK downloader is pure TCP/proxy.

## Verify

```powershell
cd sdk\js\magnetk
npm test
npm pack --dry-run
```

## License

MIT
