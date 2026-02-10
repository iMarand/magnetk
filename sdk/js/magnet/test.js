import { MagnetLink } from './magnet.js';
import { MagnetClient } from './client.js';

console.log('--- Magnet JS SDK Test ---');

// Test Magnet Link
const link = 'magnet:?xt=urn:sha256:abc123hash&dn=testfile.txt&xl=1024&relay=/ip4/127.0.0.1/tcp/4001';
const ml = MagnetLink.parse(link);
console.log('Parsed:', ml.fileHash, ml.fileName);
console.log('Generated:', ml.toString());

if (ml.toString() !== link) {
    console.error('FAIL: Magnet link roundtrip failed');
} else {
    console.log('PASS: Magnet link roundtrip');
}

// Test Client (Connect Mock)
const client = new MagnetClient();
console.log('Client initialized');
// client.download(link, '.'); // Needs running seed
