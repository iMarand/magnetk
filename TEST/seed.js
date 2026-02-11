import { spawn } from 'child_process';
import path from 'path';

const RELAY_ADDR = "/ip4/69.169.109.243/tcp/4001/p2p/12D3KooWJFPHu2bZzg7VRyYs9T2xoKkduqWUwYG2Quv5qHem66MW";
const PROJECT_ROOT = path.join(process.cwd(), '..');
const SEEDER_PATH = path.join(PROJECT_ROOT, 'bin', 'seed.exe');

async function startSeeding(filePath) {
    console.log(`--- Magnetk JS Automator: Seeding ${filePath} ---`);

    // Resolve app.txt relative to where it is (TEST/app.txt)
    const absoluteFilePath = path.resolve(process.cwd(), filePath);

    const args = [
        '-relay', RELAY_ADDR,
        '-file', absoluteFilePath
    ];

    console.log(`[JS-Automator] Executing: ${SEEDER_PATH} ${args.join(' ')}\n`);

    const seeder = spawn(SEEDER_PATH, args, { cwd: PROJECT_ROOT });

    seeder.stdout.on('data', (data) => {
        const output = data.toString();
        process.stdout.write(output);

        // Try to capture the Magnetk Link for the user
        if (output.includes('Magnetk Link:')) {
            const link = output.split('Magnetk Link:')[1].trim();
            console.log('\n[JS-Automator] 💡 COPY THIS LINK FOR DOWNLOAD:');
            console.log(link);
            console.log('--------------------------------------------\n');
        }
    });

    seeder.stderr.on('data', (data) => {
        console.error(`[Seeder Error] ${data}`);
    });

    seeder.on('close', (code) => {
        console.log(`[JS-Automator] Seeder exited with code ${code}`);
    });
}

const targetFile = process.argv[2] || 'app.txt';
startSeeding(targetFile);
