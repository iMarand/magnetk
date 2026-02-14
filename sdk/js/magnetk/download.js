import { MagnetkClient, CONNECTION_TYPE } from 'magnetk';
import path from 'path';
import os from 'os';

// Parse command line arguments
const args = process.argv.slice(2);
if (args.length < 1) {
    console.error('Usage: node download.js <magnetk_link>');
    process.exit(1);
}

const magnetURI = args[0];

// Determine Desktop Path
const desktopPath = path.join(os.homedir(), '/OneDrive/Desktop');

async function download() {
    const client = new MagnetkClient({
        dhtEnabled: true, // Enable DHT discovery
        relayUrl: '69.169.109.243',
        relayPort: 4003,
        seedPort: 4005
    });

    // Extract filename from URI roughly to show intention
    // In real download, client.download handles parsing
    let intendedFilename = 'downloaded_file';
    try {
        const urlParams = new URL(magnetURI.replace('magnetk:', 'http://dummy.com')).searchParams;
        if (urlParams.has('dn')) intendedFilename = urlParams.get('dn');
    } catch (e) { }

    const outputPath = path.join(desktopPath, intendedFilename);

    console.log(`[Download] Initiating download for: ${magnetURI}`);
    console.log(`[Download] Target: ${outputPath}`);

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
        // Show progress bar-like output
        const percent = Math.floor(p.percent);
        if (percent > lastPercent) {
            const barWidth = 20;
            const filled = Math.floor((percent / 100) * barWidth);
            const empty = barWidth - filled;
            const bar = '█'.repeat(filled) + '░'.repeat(empty);

            process.stdout.write(`\r[Download] [${bar}] ${p.percent.toFixed(1)}% | ${p.speed} KB/s | ${p.bytesDownloaded} / ${p.totalBytes} bytes`);
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

    try {
        // Use new simplified API
        await client.get(magnetURI, outputPath);
        console.log('\n\n==================================================');
        console.log('       DOWNLOAD COMPLETE!');
        console.log('==================================================');
        console.log(`File saved to: ${outputPath}`);

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
