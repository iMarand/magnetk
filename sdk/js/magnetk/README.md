# Magnetk JavaScript SDK

The professional JavaScript SDK for interacting with the Magnetk P2P network. This SDK allows you to parse/generate Magnetk links and download files via TCP using the Magnetk binary protocol.

## Installation

If using as a standalone folder, simply place the `magnetk` folder into your `node_modules` or clone it into your project.

```bash
npm install magnetk
```

## Usage

### One-Line Seeding (Zero Config)
The SDK automatically hashes your file, discovers the relay, and spawns the high-performance Go seeder in the background.

```javascript
import { MagnetkClient } from 'magnetk';

const client = new MagnetkClient({
    relayUrl: '69.169.109.243', // Optional: defaults to public relay
    relayPort: 4003
});

// Seed a file and get a shareable link
const link = await client.seed('./my-app.zip');
console.log(`Share this link: ${link}`);

// Keep the process alive to serve peers (Ctrl+C to stop)
await client.keepSeeding();
```

### One-Line Downloading
Download files with automatic connection optimization (Direct > Local > Relay).

```javascript
const magnetURI = 'magnetk:?xt=urn:sha256:abc...&relay=...';

console.log('Downloading...');
await client.download(magnetURI, './my-app-downloaded.zip');
console.log('Done!');
```

### Event-Driven Progress tracking
Track every stage of the transfer with the new Event API.

```javascript
const client = new MagnetkClient();

// Seeding Events
client.on('hashing', (p) => console.log(`Hashing file: ${p.percent}%`));
client.on('seeding', (info) => console.log(`Seeding ${info.fileName} (${info.fileSize} bytes)`));
client.on('relay-connected', (r) => console.log(`Live on Relay: ${r.host}`));

// Download Events
client.on('download-start', (info) => console.log(`Starting: ${info.fileName}`));
client.on('progress', (p) => {
    process.stdout.write(`\rDownload: ${p.percent.toFixed(1)}% | Speed: ${p.speed} KB/s`);
});
client.on('connection-type', (c) => console.log(`\nConnection Mode: ${c.type}`)); // DIRECT, RELAYED, LOCAL
client.on('complete', (info) => console.log(`\nSaved to: ${info.filePath}`));

// Error Handling
client.on('error', (err) => console.error(`Error (${err.code}): ${err.message}`));
```

## Advanced Features

### NAT Traversal (STUN)
The SDK integrates STUN to discover your public identity. You can manually check your public IP/Port:

```javascript
const identity = await client.getPublicIdentity();
console.log(`Public Identity: ${identity.ip}:${identity.port}`);
```

### Manual CLI
For quick operations, use the global `magnetk` command:

```bash
# Download a file
magnetk download "magnetk:?..." --output ./file.zip
```

## API Reference

### `MagnetkClient`
- `constructor(config)`: `{ relayUrl, relayPort, seedPort, enableSTUN }`
- `seed(filePath)`: Returns `Promise<magnetLink>`. Spawns background seeder.
- `download(uri, outputPath)`: Downloads file. Emits progress events.
- `stop()`: Kills all background seeder processes.
- `getRelayIdentity()`: Fetches Relay Peer ID and Multiaddrs.

### Events
- `hashing`: `{ percent }`
- `seeding`: `{ fileName, fileSize, hash }`
- `progress`: `{ percent, bytesDownloaded, totalBytes, speed }`
- `connection-type`: `{ type }` (DIRECT, RELAYED, LOCAL)
- `error`: `{ code, message }`

## Prerequisites

The JavaScript SDK is a lightweight wrapper around the high-performance **Magnetk Go Binaries**. You must have these binaries on your system to use seeding or relay features.

1.  **Download** the latest binaries for your platform (Windows/Linux/macOS).
2.  **Ensure** they are in your system `PATH` OR provide the path manually in the SDK config.

### Manual Configuration
```javascript
const client = new MagnetkClient({
    seederPath: 'C:\\path\\to\\seed.exe', // Explicit path
    relayUrl: '69.169.109.243'
});
```

### Environment Variables
You can also set the binary path globally:
- `MAGNETK_SEEDER_PATH`: Path to the `seed.exe` binary.

## Troubleshooting

### "spawn seed.exe ENOENT" Error
This means the SDK cannot find the Go seeder binary.
1. Check if `seed.exe` is in your system `PATH`.
2. Or provide `seederPath` in the `MagnetkClient` constructor.
3. Or set the `MAGNETK_SEEDER_PATH` environment variable.

## License
MIT

