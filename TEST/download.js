import { MagnetkClient } from './client.js';
import path from 'path';
import os from 'os';

async function runDownload() {
    console.log('--- Magnetk JS P2P Downloader ---');

    // 1. Get Magnetk Link from arguments
    const magnetURI = process.argv[2];
    if (!magnetURI) {
        console.error('Usage: npm run download -- "magnetk:?xt=urn:sha256:..."');
        console.log('\nHint: Get the link from your "npm run lookup" command or seeder output.');
        process.exit(1);
    }

    // 2. Define output path (C:\Users\iMarand\Desktop\app.txt or similar)
    // We try to use the user's desktop path.
    const desktopPath = path.join(os.homedir(), 'Desktop');

    // We'll extract the filename from the link to name the output file
    const urlParams = new URLSearchParams(magnetURI.split('?')[1]);
    const fileName = urlParams.get('dn') || 'downloaded_file.bin';
    const outputPath = path.join(desktopPath, fileName);

    console.log(`[JS-SDK] Magnet Link: ${magnetURI}`);
    console.log(`[JS-SDK] Target Path: ${outputPath}`);

    const client = new MagnetkClient();

    try {
        console.log('\nStarting P2P Transfer...');
        await client.download(magnetURI, outputPath);

        console.log('\n╔══════════════════════════════════════════════╗');
        console.log('║  DOWNLOAD SUCCESSFUL!                        ║');
        console.log('╚══════════════════════════════════════════════╝');
        console.log(`\nYour file is ready at: ${outputPath}`);
    } catch (err) {
        console.error('\n[ERROR] Download failed.');
        console.error('Possible reasons:');
        console.error(' 1. The seeder is not running (bin\\seed.exe).');
        console.error(' 2. The relay on VPS is not reachable.');
        console.error(' 3. Network/Firewall blocking port 4002.');
        console.error(`\nTechnical error: ${err.message}`);
    }
}

runDownload();
