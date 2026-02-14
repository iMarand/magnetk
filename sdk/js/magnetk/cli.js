#!/usr/bin/env node

/**
 * Magnetk CLI
 * 
 * Usage:
 *   magnetk lookup <file_hash> [--relay <host>]
 *   magnetk download <magnetk_uri> [--output <path>]
 */

import { MagnetkClient } from './client.js';
import path from 'path';
import os from 'os';

const args = process.argv.slice(2);
const command = args[0];

if (!command || command === '--help' || command === '-h' || command === 'help') {
    showHelp();
    process.exit(0);
}

const client = new MagnetkClient({
    dhtEnabled: true // Enable DHT discovery
});

async function run() {
    try {
        switch (command) {
            case 'lookup': {
                const fileHash = args[1];
                if (!fileHash) {
                    console.error('Error: Missing file hash.');
                    showHelp();
                    process.exit(1);
                }

                const relayArg = args.indexOf('--relay');
                const relay = relayArg !== -1 ? args[relayArg + 1] : '69.169.109.243';
                const port = 4003; // Default JS discovery port

                const result = await client.lookupPeer(relay, port, fileHash);

                if (result.found && result.seeds.length > 0) {
                    console.log(`\nFound ${result.seeds.length} seed(s) on relay ${relay}:`);
                    result.seeds.forEach((seed, i) => {
                        console.log(`  [${i + 1}] Peer ID: ${seed.peer_id}`);
                        console.log(`      Transport: ${seed.transport_addr || 'MsQuic/QUIC'}`);
                    });

                    // Generate a sample link if possible
                    if (result.seeds.length > 0) {
                        const seed = result.seeds[0];
                        const xt = `urn:sha256:${fileHash}`;
                        const relayAddr = `/ip4/${relay}/tcp/4001/p2p/${seed.peer_id}`; // Simplified
                        const link = `magnetk:?xt=${xt}&relay=${encodeURIComponent(relayAddr)}&dn=downloaded_file`;
                        console.log(`\nGenerated Magnetk Link:\n${link}\n`);
                    }
                } else {
                    console.log(`\nNo seeds found for hash: ${fileHash}`);
                }
                break;
            }

            case 'download': {
                const magnetURI = args[1];
                if (!magnetURI) {
                    console.error('Error: Missing Magnetk link.');
                    showHelp();
                    process.exit(1);
                }

                const outputArg = args.indexOf('--output');
                const outputPath = outputArg !== -1 ? args[outputArg + 1] : path.join(os.homedir(), 'Desktop', 'downloaded_file');

                console.log(`\n--- Magnetk P2P Download ---`);
                console.log(`Target: ${outputPath}`);

                await client.download(magnetURI, outputPath);
                break;
            }

            default:
                console.error(`Unknown command: ${command}`);
                showHelp();
                process.exit(1);
        }
    } catch (err) {
        console.error(`\n[CLI Error] ${err.message}`);
        process.exit(1);
    }
}

function showHelp() {
    console.log(`
Magnetk SDK CLI v2.0.0
----------------------
A high-performance P2P file transfer utility.

Usage:
  magnetk <command> [options]

Commands:
  lookup <hash>       Search the relay for seeds hosting a specific file hash.
  download <link>     Download a file using a Magnetk URI.

Options:
  --relay <host>      Specify a relay IP (default: 69.169.109.243)
  --output <path>     Path where to save the downloaded file (default: Desktop)
  --help, -h          Show this help message

Examples:
  magnetk lookup abc123hash
  magnetk download "magnetk:?xt=urn:sha256:abc123hash&relay=..."
`);
}

run();
