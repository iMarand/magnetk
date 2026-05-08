import { MagnetkLink } from './magnetk.js';
import { MagnetkClient } from './client.js';

console.log('--- Magnetk JS SDK Test ---');

// Test Magnetk Link
const link = 'magnetk:?xt=urn:sha256:abc123hash&dn=testfile.txt&xl=1024&relay=%2Fip4%2F127.0.0.1%2Ftcp%2F4001';
const ml = MagnetkLink.parse(link);
console.log('Parsed:', ml.fileHash, ml.fileName);
console.log('Generated:', ml.toString());

if (ml.toString() !== link) {
    console.error('FAIL: Magnetk link roundtrip failed');
    console.error('Expected:', link);
    console.error('Actual:  ', ml.toString());
} else {
    console.log('PASS: Magnetk link roundtrip');
}

// Test Client (Connect Mock)
const client = new MagnetkClient();
console.log('Client initialized');
// client.download(link, '.'); // Needs running seed
