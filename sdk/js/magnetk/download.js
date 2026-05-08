import { MagnetkClient, CONNECTION_TYPE } from './index.js';
import path from 'path';

// Parse command line arguments
const args = process.argv.slice(2);
if (args.length < 1) {
    console.error('Usage: node download.js <magnetk_link> [output_dir_or_file]');
    process.exit(1);
}

const magnetURI = args[0];
const outputTarget = args[1] || path.join(process.cwd(), 'downloads');

async function download() {
    const client = new MagnetkClient({
        relayUrl: '69.169.109.243',
        relayPort: 4003,
        seedPort: 4002,
        holePunch: true,
        relayAssisted: 'auto',
        transportMode: 'auto'
    });

    console.log(`[Download] Initiating download for: ${magnetURI}`);
    console.log(`[Download] Target: ${outputTarget}`);

    // Check Public Identity
    try {
        console.log('[Download] Resolving Public Identity (STUN)...');
        const identity = await client.getPublicIdentity();
        console.log(`[Download] Public Identity: ${identity.ip}:${identity.port}`);
    } catch (e) {
        console.warn(`[Download] STUN lookup failed: ${e.message}`);
    }

    // Progress Tracking
    client.on('download-start', (info) => console.log(`[Download] Started: ${info.fileName}`));

    let lastPercent = -1;
    client.on('progress', (p) => {
        const percent = Math.floor(p.percent);
        if (percent > lastPercent) {
            const barWidth = 20;
            const filled = Math.floor((percent / 100) * barWidth);
            const empty = barWidth - filled;
            const bar = '#'.repeat(filled) + '-'.repeat(empty);
            const chunkText = p.chunkTotalBytes
                ? ` | chunk ${p.chunkBytes}/${p.chunkTotalBytes} bytes`
                : '';

            process.stdout.write(`\r[Download] [${bar}] ${p.percent.toFixed(1)}% | ${p.speed} KB/s | ${p.bytesDownloaded} / ${p.totalBytes} bytes${chunkText}`);
            lastPercent = percent;
        }
    });

    client.on('connection-type', (c) => {
        let typeStr = c.type;
        if (c.type === CONNECTION_TYPE.DIRECT) typeStr += ' (P2P Direct)';
        if (c.type === CONNECTION_TYPE.LOCAL) typeStr += ' (Local Network)';
        if (c.type === CONNECTION_TYPE.RELAYED) typeStr += ' (Via Relay)';
        console.log(`\n[Download] Connection established: ${typeStr}`);
    });

    client.on('error', (err) => console.error(`\n[Download] Error: ${err.message}`));
    let savedPath = outputTarget;
    client.on('complete', (info) => {
        savedPath = info.filePath;
    });

    try {
        await client.get(magnetURI, outputTarget);
        console.log('\n\n==================================================');
        console.log('       DOWNLOAD COMPLETE!');
        console.log('==================================================');
        console.log(`File saved to: ${savedPath}`);

        // Show stats
        const stats = client.lastDownloadStats;
        console.log('Final Stats:');
        console.log(`- Connection Type: ${stats.connectionType}`);
        console.log(`- Remote Address: ${stats.remoteAddr}`);

    } catch (err) {
        console.error('\n[Download] Failed:', err);
        process.exit(1);
    }
}

download();
