import { MagnetkClient } from './client.js';
import { MagnetkLink } from './magnetk.js';

const VPS_IP = '69.169.109.243';
const RELAY_DISCOVERY_PORT = 4001; // LibP2P Discovery (TCP)
const RELAY_DATA_PORT = 4002;      // MsQuic Data (UDP - JS net module can't connect via TCP)

async function runTest() {
    console.log('--- Magnetk JS SDK VPS Connection Test ---');
    const client = new MagnetkClient();

    console.log(`[Test] Attempting to connect to VPS Discovery Port (${VPS_IP}:${RELAY_DISCOVERY_PORT})...`);

    try {
        const socket = await client.connect(VPS_IP, RELAY_DISCOVERY_PORT);
        console.log('[Test] SUCCESS: TCP Connection established to Discovery Port!');

        // Protocol handshake check (LibP2P uses multistream-select)
        // We just close it for now as a basic connectivity test
        socket.end();
        console.log('[Test] Connection closed.');
    } catch (err) {
        console.error('[Test] FAILED: Could not connect to Discovery Port.');
        console.error(err.message);
    }

    console.log('\n[Test] Initializing Magnetk Link check...');
    const testLink = `magnetk:?xt=urn:sha256:abc123hash&dn=vps-test.txt&xl=1024&relay=/ip4/${VPS_IP}/tcp/${RELAY_DISCOVERY_PORT}`;
    const ml = MagnetkLink.parse(testLink);
    console.log(`[Test] Parsed Link for: ${ml.fileName}`);
    console.log(`[Test] Relay Address: ${ml.relayAddr}`);

    console.log('\n--- Test Completed ---');
}

runTest().catch(console.error);
