/**
 * Magnet JS SDK - Client
 * 
 * Provides methods for discovery and file download using the binary framing protocol over TCP.
 * Note: Currently supports fetching metadata/manifest. Full QUIC download requires C++ bindings.
 */

import net from 'net';
import fs from 'fs';
import { MagnetkLink } from './magnetk.js';

const MSG_TYPE = {
    MSG_LOOKUP_PEER: 0x03,
    MSG_LOOKUP_RESULT: 0x04,
    MSG_MANIFEST_REQ: 0x10,
    MSG_MANIFEST_RES: 0x11,
    MSG_CHUNK_REQ: 0x20,
    MSG_CHUNK_RES: 0x21,
    MSG_ERROR: 0xFF
};

export class MagnetkClient {
    constructor(config = {}) {
        this.config = config;
        this.connections = new Map();
    }

    /**
     * Connects to a seed via TCP.
     * @param {string} host 
     * @param {number} port 
     * @returns {Promise<net.Socket>}
     */
    connect(host, port) {
        return new Promise((resolve, reject) => {
            const socket = net.createConnection(port, host, () => {
                console.log(`[Magnetk-JS] Connected to ${host}:${port}`);
                resolve(socket);
            });
            socket.on('error', (err) => {
                console.error(`[Magnetk-JS] Connection error: ${err.message}`);
                reject(err);
            });
        });
    }

    /**
     * Look up seeds for a file hash from the relay.
     * @param {string} relayHost 
     * @param {number} relayPort 
     * @param {string} fileHash 
     * @returns {Promise<Object>}
     */
    async lookupPeer(relayHost, relayPort, fileHash) {
        console.log(`[Magnetk-JS] Looking up seeds for ${fileHash.substring(0, 10)} on relay ${relayHost}:${relayPort}...`);
        const socket = await this.connect(relayHost, relayPort);
        const writer = new FrameWriter(socket);
        const reader = new FrameReader(socket);

        try {
            await writer.writeJSON(MSG_TYPE.MSG_LOOKUP_PEER, { file_hash: fileHash });
            const frame = await reader.readFrame();

            if (frame.type === MSG_TYPE.MSG_LOOKUP_RESULT) {
                const result = JSON.parse(frame.payload.toString());
                return result;
            } else if (frame.type === MSG_TYPE.MSG_ERROR) {
                const err = JSON.parse(frame.payload.toString());
                throw new Error(`Relay error: ${err.message}`);
            } else {
                throw new Error(`Unexpected response type: 0x${frame.type.toString(16)}`);
            }
        } finally {
            socket.end();
        }
    }

    /**
     * Downloads a file from a magnetk link.
     * @param {string} magnetURI 
     * @param {string} outputPath 
     */
    async download(magnetURI, outputPath) {
        const ml = MagnetkLink.parse(magnetURI);
        console.log(`[Magnetk-JS] Starting download for: ${ml.fileName}`);

        // 1. Resolve seed address from the Magnetk Link
        // For now, the link contains the relay address. In a full system, 
        // we'd query the relay for seeds. For this demo, we assume the seed 
        // is at the same host as the relay's TCP discovery port (simplified).
        // The user manually provided the relay IP.
        const seedHost = ml.relayAddr.split('/')[2];
        const seedPort = 4002; // Transport port (TCP fallback)

        console.log(`[Magnetk-JS] Connecting to seed at ${seedHost}:${seedPort} (TCP)...`);

        const socket = await this.connect(seedHost, seedPort);
        const writer = new FrameWriter(socket);
        const reader = new FrameReader(socket);

        try {
            // STEP 1: Request manifest
            console.log(`[Magnetk-JS] Requesting manifest for ${ml.fileHash.substring(0, 10)}...`);
            await writer.writeJSON(MSG_TYPE.MSG_MANIFEST_REQ, { file_hash: ml.fileHash });

            const manifestFrame = await reader.readFrame();
            if (manifestFrame.type !== MSG_TYPE.MSG_MANIFEST_RES) {
                throw new Error(`Unexpected manifest response type: 0x${manifestFrame.type.toString(16)}`);
            }

            const manifest = JSON.parse(manifestFrame.payload.toString());
            console.log(`[Magnetk-JS] Manifest received: ${manifest.file_name}, ${manifest.total_chunks} chunks, total size: ${manifest.file_size} bytes`);

            // STEP 2: Prepare output file
            const fd = fs.openSync(outputPath, 'w');

            // STEP 3: Request and write chunks
            for (let i = 0; i < manifest.total_chunks; i++) {
                process.stdout.write(`\r[Magnetk-JS] Downloading chunk ${i + 1}/${manifest.total_chunks}...`);

                await writer.writeJSON(MSG_TYPE.MSG_CHUNK_REQ, {
                    file_hash: ml.fileHash,
                    chunk_indices: [i]
                });

                const chunkFrame = await reader.readFrame();
                if (chunkFrame.type !== MSG_TYPE.MSG_CHUNK_RES) {
                    throw new Error(`Unexpected chunk response type: 0x${chunkFrame.type.toString(16)}`);
                }

                // Chunk payload format: [4 bytes: index][raw data]
                const index = chunkFrame.payload.readUInt32BE(0);
                const data = chunkFrame.payload.slice(4);

                // Write to file at correct offset
                const offset = i * manifest.chunk_size;
                fs.writeSync(fd, data, 0, data.length, offset);
            }

            fs.closeSync(fd);
            console.log(`\n[Magnetk-JS] ✓ Download complete: ${outputPath}`);

        } catch (err) {
            console.error(`[Magnetk-JS] Download failed: ${err.message}`);
            throw err;
        } finally {
            socket.end();
        }
    }
}

class FrameWriter {
    constructor(socket) {
        this.socket = socket;
    }

    async writeFrame(type, payload) {
        const header = Buffer.alloc(5);
        header.writeUInt32BE(payload.length, 0);
        header.writeUInt8(type, 4);

        this.socket.write(header);
        this.socket.write(payload);
    }

    async writeJSON(type, obj) {
        const payload = Buffer.from(JSON.stringify(obj));
        await this.writeFrame(type, payload);
    }
}

class FrameReader {
    constructor(socket) {
        this.socket = socket;
        this.buffer = Buffer.alloc(0);
        this.pendingResolve = null;
        this.socket.on('data', (chunk) => this._onData(chunk));
    }

    _onData(chunk) {
        this.buffer = Buffer.concat([this.buffer, chunk]);
        this._tryResolve();
    }

    _tryResolve() {
        if (!this.pendingResolve) return;

        if (this.buffer.length >= 5) {
            const len = this.buffer.readUInt32BE(0);
            const type = this.buffer.readUInt8(4);

            if (this.buffer.length >= 5 + len) {
                const payload = this.buffer.slice(5, 5 + len);
                this.buffer = this.buffer.slice(5 + len);

                const resolve = this.pendingResolve;
                this.pendingResolve = null;
                resolve({ type, payload });

                // Try again for any buffered data
                this._tryResolve();
            }
        }
    }

    readFrame() {
        return new Promise((resolve) => {
            this.pendingResolve = resolve;
            this._tryResolve();
        });
    }
}
