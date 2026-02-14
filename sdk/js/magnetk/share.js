import { MagnetkClient } from 'magnetk';
import path from 'path';
import 'dotenv/config';

const filePath = "./app.txt";

async function share() {
    const client = new MagnetkClient({
        relayUrl: '69.169.109.243', // Default to local for dev, change to public IPs for prod
        relayPort: 4003,
        seederPath: process.env.MAGNETK_SEEDER_PATH,
        seedPort: 4005 // Default seed port
    });

    console.log(`[Share] Preparing to share: ${filePath}`);

    // Check Public Identity
    try {
        console.log('[Share] Resolving Public Identity (STUN)...');
        const identity = await client.getPublicIdentity();
        console.log(`[Share] Public Identity: ${identity.ip}:${identity.port}`);
    } catch (e) {
        console.warn(`[Share] STUN lookup failed: ${e.message}`);
    }

    // Event listeners for feedback
    client.on('validating-relay', () => console.log('[Share] Connecting to relay...'));
    client.on('hashing', (p) => {
        if (p.percent % 20 === 0 || p.percent === 100) {
            console.log(`[Share] Hashing file... ${p.percent}%`);
        }
    });
    client.on('relay-connected', (info) => console.log(`[Share] Connected to Relay at ${info.host}`));
    client.on('seeding', (info) => {
        console.log(`[Share] Seeding active: ${info.fileName} (${info.fileSize} bytes)`);
        console.log(`[Share] File Hash: ${info.hash}`);
    });
    client.on('error', (err) => console.error(`[Share] Error: ${err.message}`));

    try {
        // Use new simplified API
        const link = await client.share(filePath);

        console.log('\n==================================================');
        console.log('       FILE SHARED SUCCESSFULLY!');
        console.log('==================================================');
        console.log('\nmagnet link:');
        console.log(link);
        console.log('\nTo download, run:');
        console.log(`node download.js "${link}"`);
        console.log('\n[Share] Seeder running in background. Press Ctrl+C to stop.');

        // Keep process alive
        await client.keepSeeding();
    } catch (err) {
        console.error('[Share] Failed:', err);
        client.stop();
        process.exit(1);
    }
}

share();
