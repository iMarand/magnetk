# Magnetk SDK - Ideal Design

This document shows how the Magnetk SDK **should** work with clean, minimal code that's comfortable for developers.

---

## 1. Simple Upload (Auto-hash, Auto-seed)

```javascript
import { MagnetkClient } from 'magnetk';

const client = new MagnetkClient({
  relayUrl: '69.169.109.243',
  relayPort: 4003
});

// ONE LINE - Upload and get link
const link = await client.seed('./app.txt');

console.log(`Share: ${link}`);
// Output: magnetk:?xt=urn:sha256:8328c36d...&dn=app.txt&xl=8&relay=/ip4/69.169.109.243/tcp/4003

// File is now being seeded automatically!
```

**What happens internally:**
- ✅ Auto-calculates SHA256 hash
- ✅ Auto-detects file size
- ✅ Validates relay connection
- ✅ Starts seeding automatically
- ✅ Returns shareable link

---

## 2. Simple Download

```javascript
import { MagnetkClient } from 'magnetk';

const client = new MagnetkClient({
  relayUrl: '69.169.109.243',
  relayPort: 4003
});

// ONE LINE - Download file
await client.download(
  'magnetk:?xt=urn:sha256:8328c36d...&dn=app.txt&xl=8&relay=/ip4/69.169.109.243/tcp/4003',
  './downloads/app.txt'
);

console.log('Download complete!');
```

**What happens internally:**
- ✅ Parses the magnetk link
- ✅ Validates relay connection
- ✅ Attempts STUN (NAT traversal)
- ✅ Connects to peer (direct or relay)
- ✅ Downloads and saves file

---

## 3. Upload with Progress

```javascript
import { MagnetkClient } from 'magnetk';

const client = new MagnetkClient({
  relayUrl: '69.169.109.243',
  relayPort: 4003
});

// Listen for events
client.on('seeding', (info) => {
  console.log(`Seeding: ${info.fileName} (${info.fileSize} bytes)`);
});

client.on('peer-connected', (peer) => {
  console.log(`Peer connected: ${peer.peerId}`);
});

// Seed the file
const link = await client.seed('./video.mp4');
console.log(`Link: ${link}`);

// Keep seeding
await client.keepSeeding();
```

---

## 4. Download with Progress

```javascript
import { MagnetkClient } from 'magnetk';

const client = new MagnetkClient({
  relayUrl: '69.169.109.243',
  relayPort: 4003
});

// Listen for progress
client.on('progress', ({ percent, speed }) => {
  console.log(`${percent}% - ${speed} MB/s`);
});

client.on('connection-type', ({ type }) => {
  console.log(`Connection: ${type}`); // DIRECT, LOCAL, or RELAYED
});

// Download
await client.download(magnetkLink, './output.mp4');
```

---

## 5. Full Configuration (Optional)

```javascript
import { MagnetkClient } from 'magnetk';

const client = new MagnetkClient({
  // Required
  relayUrl: '69.169.109.243',
  
  // Optional
  relayPort: 4003,
  seedPort: 4002,
  downloadPath: './downloads',
  timeout: 30000,
  maxRetries: 3,
  chunkSize: 1024 * 1024, // 1MB
  
  // STUN
  enableSTUN: true,
  stunServers: ['stun:stun.l.google.com:19302']
});
```

---

## 6. Check Public IP (STUN)

```javascript
import { MagnetkClient } from 'magnetk';

const client = new MagnetkClient({
  relayUrl: '69.169.109.243',
  enableSTUN: true
});

const identity = await client.getPublicIdentity();

console.log(`Public IP: ${identity.ip}:${identity.port}`);
console.log(`NAT Type: ${identity.natType}`);
```

---

## 7. Lookup Seeds

```javascript
import { MagnetkClient } from 'magnetk';

const client = new MagnetkClient({
  relayUrl: '69.169.109.243',
  relayPort: 4003
});

const fileHash = '8328c36d18b7834a38118f6ec924ae143c10263f2519c723ccb36ca14e7461fb';

const seeds = await client.lookupSeeds(fileHash);

console.log(`Found ${seeds.length} seeds`);
seeds.forEach(seed => {
  console.log(`  ${seed.peerId} at ${seed.address}`);
});
```

---

## 8. Error Handling (Simple)

```javascript
import { MagnetkClient } from 'magnetk';

const client = new MagnetkClient({
  relayUrl: '69.169.109.243'
});

try {
  const link = await client.seed('./file.zip');
  console.log(`Success: ${link}`);
} catch (error) {
  if (error.code === 'RELAY_UNREACHABLE') {
    console.error('Cannot connect to relay server');
  } else if (error.code === 'FILE_NOT_FOUND') {
    console.error('File does not exist');
  } else {
    console.error(error.message);
  }
}
```

---

## 9. Complete Example - Upload

```javascript
import { MagnetkClient } from 'magnetk';

async function uploadFile(filePath) {
  const client = new MagnetkClient({
    relayUrl: '69.169.109.243',
    relayPort: 4003
  });
  
  // Event listeners
  client.on('validating-relay', () => {
    console.log('Checking relay...');
  });
  
  client.on('hashing', (progress) => {
    console.log(`Hashing: ${progress}%`);
  });
  
  client.on('seeding', (info) => {
    console.log(`Now seeding: ${info.fileName}`);
  });
  
  // Seed file
  const link = await client.seed(filePath);
  
  console.log('═'.repeat(50));
  console.log('Share this link:');
  console.log(link);
  console.log('═'.repeat(50));
  
  // Keep seeding
  await client.keepSeeding();
}

uploadFile('./app.txt');
```

**Output:**
```
Checking relay...
✓ Relay is reachable
Hashing: 100%
Now seeding: app.txt
══════════════════════════════════════════════════
Share this link:
magnetk:?xt=urn:sha256:8328c36d...&dn=app.txt
══════════════════════════════════════════════════
Press Ctrl+C to stop seeding
```

---

## 10. Complete Example - Download

```javascript
import { MagnetkClient } from 'magnetk';

async function downloadFile(magnetkLink, outputPath) {
  const client = new MagnetkClient({
    relayUrl: '69.169.109.243',
    relayPort: 4003,
    enableSTUN: true
  });
  
  // Event listeners
  client.on('connection-type', ({ type }) => {
    console.log(`Connection: ${type}`);
  });
  
  client.on('progress', ({ percent, bytesDownloaded, totalBytes, speed }) => {
    process.stdout.write(`\rProgress: ${percent.toFixed(1)}% | Speed: ${speed} KB/s`);
  });
  
  client.on('complete', () => {
    console.log('\n✓ Download complete!');
  });
  
  // Download
  await client.download(magnetkLink, outputPath);
}

const link = 'magnetk:?xt=urn:sha256:8328c36d...&dn=app.txt';
downloadFile(link, './downloads/app.txt');
```

**Output:**
```
Connection: DIRECT
Progress: 45.2% | Speed: 1250 KB/s
```

---

## Key Improvements Needed in SDK

### Current SDK (Too Complex)
```javascript
// Current - TOO MANY STEPS
const link = MagnetkLink.parse(uri);
const fileHash = calculateHash(file);
const fileSize = getFileSize(file);
const magnetkLink = new MagnetkLink(hash, name, size, peer, relay);
const uri = magnetkLink.toString();
// Then manually start Go seeder...
```

### Ideal SDK (Simple)
```javascript
// Ideal - ONE LINE
const link = await client.seed('./file.zip');
```

---

## SDK Methods Summary

| Method | Description | Code |
|--------|-------------|------|
| `seed(filePath)` | Upload and share a file | `await client.seed('./file.zip')` |
| `download(link, output)` | Download a file | `await client.download(link, './out.zip')` |
| `keepSeeding()` | Keep file available | `await client.keepSeeding()` |
| `lookupSeeds(hash)` | Find available seeders | `await client.lookupSeeds(hash)` |
| `getPublicIdentity()` | Get public IP via STUN | `await client.getPublicIdentity()` |
| `stop()` | Stop seeding | `client.stop()` |

---

## Events Summary

| Event | When | Data |
|-------|------|------|
| `validating-relay` | Checking relay | none |
| `relay-connected` | Relay OK | `{ host, port }` |
| `hashing` | Hashing file | `{ percent }` |
| `seeding` | File ready | `{ fileName, fileSize, hash }` |
| `peer-connected` | Peer joined | `{ peerId, address }` |
| `connection-type` | Connection established | `{ type }` (DIRECT/LOCAL/RELAYED) |
| `progress` | Download progress | `{ percent, bytesDownloaded, totalBytes, speed }` |
| `complete` | Transfer done | `{ filePath }` |
| `error` | Error occurred | `{ code, message }` |

---

## Implementation Priority

### Phase 1: Core Simplification
1. ✅ Auto-hash files in `seed()`
2. ✅ Auto-validate relay in constructor
3. ✅ Auto-start seeding internally
4. ✅ Auto-parse links in `download()`

### Phase 2: Developer Experience
1. ✅ Add event emitters
2. ✅ Better error codes
3. ✅ Progress callbacks
4. ✅ Automatic retries

### Phase 3: Advanced Features
1. ✅ Multiple file seeding
2. ✅ Resume downloads
3. ✅ Peer discovery
4. ✅ Bandwidth limits

---

## Design Principles

1. **Simple by default** - Common tasks should be 1-3 lines
2. **Auto-everything** - Hash, validate, connect automatically
3. **Event-driven** - Use events for progress/status
4. **Fail gracefully** - Clear error codes and messages
5. **Hide complexity** - Internal details hidden from users

---

## Before vs After

### BEFORE (Current - Complicated)
```javascript
import { MagnetkLink } from 'magnetk';
import { createHash } from 'crypto';
import { readFileSync, statSync } from 'fs';

const fileBuffer = readFileSync('./app.txt');
const hash = createHash('sha256').update(fileBuffer).digest('hex');
const fileSize = statSync('./app.txt').size;
const link = new MagnetkLink(hash, 'app.txt', fileSize, peerId, relay);
const uri = link.toString();
// Then manually run: ./bin/seed.exe -relay "..." -file "..."
```

### AFTER (Ideal - Simple)
```javascript
import { MagnetkClient } from 'magnetk';

const client = new MagnetkClient({ relayUrl: '69.169.109.243' });
const link = await client.seed('./app.txt');
console.log(link);
```

**91% less code, infinitely easier!**