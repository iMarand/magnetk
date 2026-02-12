import { MagnetkClient } from './client.js';
import { MagnetkLink } from './magnetk.js';

const VPS_IP = '127.0.0.1';
const RELAY_PORT = 4003;

async function runLookup() {
    console.log('--- Magnetk JS Discovery Lookup ---');

    // Get file hash from arguments or use a default one for demo
    const fileHash = process.argv[2];
    if (!fileHash) {
        console.error('Usage: npm run lookup -- <file_hash>');
        console.log('\nExample: npm run lookup -- abc123hash');
        console.log('\nHint: Use the hash from your seeder output.');
        process.exit(1);
    }

    const client = new MagnetkClient();

    try {
        const result = await client.lookupPeer(VPS_IP, RELAY_PORT, fileHash);

        if (result && result.found) {
            console.log('\n[PASS] Seeds found for file!');
            console.log(` - File: ${result.file_name || 'unknown'}`);
            console.log(` - Size: ${result.file_size || '0'} bytes`);
            console.log(` - Seeds: ${result.seeds.length}`);

            // Generate a real Magnetk Link from the result
            const ml = new MagnetkLink(
                fileHash,
                result.file_name || 'downloaded_file',
                result.file_size || 0,
                result.seeds[0].peer_id, // Primary seed
                `/ip4/${VPS_IP}/tcp/${RELAY_PORT}`
            );

            const link = ml.toString();
            console.log('\n╔══════════════════════════════════════════════╗');
            console.log('║  GENERATED MAGNETK LINK                      ║');
            console.log('╚══════════════════════════════════════════════╝');
            console.log(`\n${link}`);
            console.log('\nUse this link with "npm run download"');
        } else {
            console.log('\n[FAIL] No seeds found on relay for this hash.');
            console.log('Ensure your seeder (bin\\seed.exe) is running and connected.');
        }
    } catch (err) {
        console.error(`\n[ERROR] Lookup failed: ${err.message}`);
    }
}

runLookup().catch(console.error);
