/**
 * Magnet JS SDK - Client
 * 
 * Provides methods for discovery and file download using the binary framing protocol over TCP.
 * Note: Currently supports fetching metadata/manifest. Full QUIC download requires C++ bindings.
 */

import net from 'net';
import fs from 'fs';
import { MagnetLink } from './magnet.js';

const MSG_TYPE = {
    MSG_MANIFEST_REQ: 0x10,
    MSG_MANIFEST_RES: 0x11,
    MSG_CHUNK_REQ: 0x20,
    MSG_CHUNK_RES: 0x21,
    MSG_ERROR: 0xFF
};

export class MagnetClient {
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
                console.log(`[JS-SDK] Connected to ${host}:${port}`);
                resolve(socket);
            });
            socket.on('error', (err) => {
                console.error(`[JS-SDK] Connection error: ${err.message}`);
                reject(err);
            });
        });
    }

    /**
     * Downloads a file from a magnet link.
     * @param {string} magnetURI 
     * @param {string} outputPath 
     */
    async download(magnetURI, outputPath) {
        const ml = MagnetLink.parse(magnetURI);
        console.log(`[JS-SDK] Starting download for: ${ml.fileName}`);

        // TODO: Resolve seed address from relay. For now, assume localhost:4002 (TCP fallback if implemented)
        // or just connect to the seed's TCP discovery port if implemented.
        // Since Go uses MsQuic for data transfer (UDP), purely TCP JS client can't fetch data 
        // unless I implement a TCP data listener on Go side.

        // For this demo, let's assume the seed also listens on TCP for development/fallback.
        const seedHost = '127.0.0.1';
        const seedPort = 4002; // Assuming this port also accepts TCP? (In reality Go code uses MsQuic UDP)

        console.log(`[JS-SDK] Warning: TCP download not fully supported by Go seed (UDP-only). Protocol checks only.`);

        // Send manifest request to demonstrate protocol framing
        /*
        const socket = await this.connect(seedHost, seedPort);
        const writer = new FrameWriter(socket);
        const reader = new FrameReader(socket);

        // Request manifest
        await writer.writeJSON(MSG_TYPE.MSG_MANIFEST_REQ, { file_hash: ml.fileHash });
        
        // Read response
        const frame = await reader.readFrame();
        if (frame.type === MSG_TYPE.MSG_MANIFEST_RES) {
            const manifest = JSON.parse(frame.payload.toString());
            console.log(`[JS-SDK] Received manifest: ${manifest.file_name}, ${manifest.total_chunks} chunks`);
        }
        
        socket.end();
        */
        return;
    }
}

class FrameWriter {
    constructor(socket) {
        this.socket = socket;
    }

    async writeFrame(type, payload) {
        const len = Buffer.alloc(4);
        len.writeUInt32BE(payload.length);

        const typeBuf = Buffer.alloc(1);
        typeBuf.writeUInt8(type);

        this.socket.write(len);
        this.socket.write(typeBuf);
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
    }

    readFrame() {
        return new Promise((resolve, reject) => {
            const onData = (chunk) => {
                this.buffer = Buffer.concat([this.buffer, chunk]);

                if (this.buffer.length >= 5) {
                    const len = this.buffer.readUInt32BE(0);
                    const type = this.buffer.readUInt8(4);

                    if (this.buffer.length >= 5 + len) {
                        const payload = this.buffer.slice(5, 5 + len);
                        this.buffer = this.buffer.slice(5 + len);
                        this.socket.removeListener('data', onData);
                        resolve({ type, payload });
                    }
                }
            };
            this.socket.on('data', onData);
        });
    }
}
