# Magnetk JavaScript SDK

The professional JavaScript SDK for interacting with the Magnetk P2P network. This SDK allows you to parse/generate Magnetk links and download files via TCP using the Magnetk binary protocol.

## Installation

If using as a standalone folder, simply place the `magnetk` folder into your `node_modules` or clone it into your project.

```bash
npm install magnetk-sdk-js
```

## Usage

### Parsing a Magnetk Link

```javascript
import { MagnetkLink } from 'magnetk';

const uri = 'magnetk:?xt=urn:sha256:abc123...&dn=myfile.zip&xl=1048576&relay=1.2.3.4';
const link = MagnetkLink.parse(uri);

console.log(link.fileName); // myfile.zip
console.log(link.fileHash); // abc123...
```

### Generating a Magnetk Link

```javascript
import { MagnetkLink } from 'magnetk';

const link = new MagnetkLink(
    '98df9a59...',           // File Hash
    'app.txt',              // File Name
    4,                      // File Size (bytes)
    '12D3KooWDmX2...',      // Seed Peer ID
    '/ip4/1.2.3.4/tcp/4001' // Relay Multiaddr
);

console.log(link.toString());
// Output: magnetk:?dn=app.txt&relay=%2Fip4%2F1.2.3.4...
```

### Downloading a File

```javascript
import { MagnetkClient, CONNECTION_TYPE } from 'magnetk';

const client = new MagnetkClient();
const uri = 'magnetk:?xt=urn:sha256:98df9a...&dn=app.txt&relay=/ip4/1.2.3.4/tcp/4001';

try {
    console.log('Starting P2P download...');
    await client.download(uri, './downloads/app.txt');

    // Access connection diagnostics
    const stats = client.lastDownloadStats;
    console.log(`Success! Connection Type: ${stats.connectionType}`);
    console.log(`Remote Address: ${stats.remoteAddr}`);
} catch (err) {
    console.error('Download failed:', err.message);
}
```

### Checking Connection Types

The SDK automatically tracks the quality of your P2P connection:
- `CONNECTION_TYPE.DIRECT`: High-speed P2P (via STUN discovered public IP).
- `CONNECTION_TYPE.LOCAL`: Super-fast local transfer (same machine/LAN).
- `CONNECTION_TYPE.RELAYED`: Connection bridged via relay (fallback).

## Advanced Features

### NAT Traversal (STUN)
The SDK integrates STUN to discover your public identity. You can manually check your public IP/Port:

```javascript
const identity = await client.getPublicIdentity();
console.log(`Public Identity: ${identity.ip}:${identity.port}`);
```

### Seeding (Distributing Files)
To share a file on the Magnetk network, use the high-performance **Go Seeder** included in the toolkit:

```bash
# Seed a file via the relay
./bin/seed.exe -relay "/ip4/relay-ip/tcp/4001" -file "path/to/file.zip"
```

## CLI Interface

For quick operations, use the global `magnetk` command:

```bash
# Lookup seeds for a hash
magnetk lookup 98df9a59... --relay 1.2.3.4

# Download a file
magnetk download "magnetk:?..." --output ./file.zip
```

## API Reference

### `MagnetkLink`
- `static parse(uri)`: Create a link object from string.
- `toString()`: Serialize back to string.

### `MagnetkClient`
- `download(uri, outputPath)`: High-level P2P download.
- `getPublicIdentity()`: Resolve public IP via STUN.
- `lastDownloadStats`: Metadata from the previous download.
- `lookupPeer(relayHost, relayPort, fileHash)`: Query relay for seeds.

## License
MIT
