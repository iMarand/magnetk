import { MagnetkClient } from './index.js';
import fs from 'fs';
import path from 'path';

async function testIdealFlow() {
    const client = new MagnetkClient({
        relayUrl: '127.0.0.1',
        relayPort: 4003,
        seedPort: 4002
    });

    const testFile = './test_data.txt';
    const downloadFile = './test_downloaded.txt';
    fs.writeFileSync(testFile, 'Hello Magnetk Ideal Design! ' + Date.now());

    console.log('--- Phase 1: Seeding ---');
    client.on('hashing', (p) => console.log(`Hashing: ${p.percent}%`));
    client.on('seeding', (info) => console.log(`Seeding started: ${info.fileName}`));

    try {
        const link = await client.seed(testFile);
        console.log(`Generated Link: ${link}`);

        console.log('\n--- Phase 2: Downloading ---');
        const dlClient = new MagnetkClient({
            relayUrl: '127.0.0.1',
            relayPort: 4003
        });

        dlClient.on('progress', (p) => {
            process.stdout.write(`\rDownload Progress: ${p.percent.toFixed(1)}% | Speed: ${p.speed} KB/s`);
        });

        dlClient.on('connection-type', (c) => console.log(`\nConnection: ${c.type}`));

        await dlClient.download(link, downloadFile);
        console.log('\n✓ Download complete!');

        const original = fs.readFileSync(testFile, 'utf8');
        const downloaded = fs.readFileSync(downloadFile, 'utf8');

        if (original === downloaded) {
            console.log('SUCCESS: File content matches!');
        } else {
            console.error('ERROR: Content mismatch!');
        }

    } catch (err) {
        console.error('Test Failed:', err);
    } finally {
        client.stop();
        if (fs.existsSync(testFile)) fs.unlinkSync(testFile);
        if (fs.existsSync(downloadFile)) fs.unlinkSync(downloadFile);
        process.exit(0);
    }
}

testIdealFlow();
