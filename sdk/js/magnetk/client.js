/**
 * Magnet JS SDK - Client
 * 
 * Provides methods for discovery and file download using the binary framing protocol over TCP.
 * Note: Currently supports fetching metadata/manifest. Full QUIC download requires C++ bindings.
 */

import net from 'net';
import fs from 'fs';
import dgram from 'dgram';
import crypto from 'crypto';
import path from 'path';
import { spawn } from 'child_process';
import { EventEmitter } from 'events';
import { fileURLToPath } from 'url';
import { MagnetkLink } from './magnetk.js';

const MSG_TYPE = {
    MSG_LOOKUP_PEER: 0x03,
    MSG_LOOKUP_RESULT: 0x04,
    MSG_MANIFEST_REQ: 0x10,
    MSG_MANIFEST_RES: 0x11,
    MSG_CHUNK_REQ: 0x20,
    MSG_CHUNK_RES: 0x21,
    MSG_GET_IDENTITY: 0x32,
    MSG_IDENTITY_RES: 0x33,
    MSG_ERROR: 0xFF
};

export const CONNECTION_TYPE = {
    DIRECT: 'DIRECT',
    RELAYED: 'RELAYED',
    LOCAL: 'LOCAL',
    UNKNOWN: 'UNKNOWN'
};

export class MagnetkClient extends EventEmitter {
    constructor(config = {}) {
        super();
        this.config = {
            relayUrl: '69.169.109.243',
            relayPort: 4003,
            seedPort: 4002,
            enableSTUN: true,
            seederPath: null,
            configPath: null,
            ...config
        };
        this.processes = [];
        this.lastDownloadStats = {
            connectionType: CONNECTION_TYPE.UNKNOWN,
            remoteAddr: null,
            duration: 0,
            bytesReceived: 0
        };
    }

    /**
     * Retrieves the public IP and port using STUN.
     * @returns {Promise<{ip: string, port: number}>}
     */
    getPublicIdentity(stunServer = 'stun.l.google.com', stunPort = 19302) {
        return new Promise((resolve, reject) => {
            const socket = dgram.createSocket('udp4');
            const message = Buffer.alloc(20);
            message.writeUInt16BE(0x0001, 0); // Binding Request
            message.writeUInt16BE(0x0000, 2); // Length
            message.writeUInt32BE(0x2112A442, 4); // Magic Cookie
            for (let i = 8; i < 20; i++) message[i] = Math.floor(Math.random() * 256);

            const timeout = setTimeout(() => {
                socket.close();
                reject(new Error('STUN discovery timed out'));
            }, 5000);

            socket.on('message', (msg) => {
                let offset = 20;
                while (offset < msg.length) {
                    const attrType = msg.readUInt16BE(offset);
                    const attrLen = msg.readUInt16BE(offset + 2);
                    if (attrType === 0x0020) { // XOR-MAPPED-ADDRESS
                        const port = msg.readUInt16BE(offset + 6) ^ 0x2112;
                        const ip = [];
                        for (let i = 0; i < 4; i++) ip.push(msg[offset + 8 + i] ^ message[4 + i]);
                        clearTimeout(timeout);
                        socket.close();
                        resolve({ ip: ip.join('.'), port });
                        return;
                    }
                    offset += 4 + attrLen;
                }
            });

            socket.on('error', (err) => {
                clearTimeout(timeout);
                socket.close();
                reject(err);
            });

            socket.send(message, 0, message.length, stunPort, stunServer);
        });
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
     * Retrieves the relay's Peer ID and multiaddrs via TCP.
     */
    async getRelayIdentity() {
        console.log(`[Magnetk-JS] Fetching relay identity from ${this.config.relayUrl}:${this.config.relayPort}...`);
        const socket = await this.connect(this.config.relayUrl, this.config.relayPort);
        const writer = new FrameWriter(socket);
        const reader = new FrameReader(socket);

        try {
            await writer.writeFrame(MSG_TYPE.MSG_GET_IDENTITY, Buffer.alloc(0)); // Empty payload
            const frame = await reader.readFrame();

            if (frame.type === MSG_TYPE.MSG_IDENTITY_RES) {
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
     * Seeds a file and returns a Magnetk link.
     * @param {string} filePath 
     * @returns {Promise<string>}
     */
    async seed(filePath) {
        const absolutePath = path.resolve(filePath);
        if (!fs.existsSync(absolutePath)) {
            throw new Error(`File not found: ${absolutePath}`);
        }

        this.emit('validating-relay');
        // Simple reachability check (optional but good for UX)

        const fileStats = fs.statSync(absolutePath);
        this.emit('hashing', { percent: 0 });

        // Calculate hash
        const hash = await this._calculateHash(absolutePath);
        this.emit('hashing', { percent: 100 });

        // Fetch Relay Identity to get Peer ID
        let relayPeerId;
        try {
            const identity = await this.getRelayIdentity();
            relayPeerId = identity.peer_id;
            console.log(`[Magnetk-JS] Relay Peer ID: ${relayPeerId}`);
        } catch (e) {
            console.warn(`[Magnetk-JS] Failed to fetch relay identity: ${e.message}. Using default/configured address might fail if Peer ID is missing.`);
        }

        const isWin = process.platform === 'win32';
        const binName = isWin ? 'seed.exe' : 'seed';
        const binPath = this._resolveBinPath(binName);
        const configPath = this._resolveConfigPath();

        // Construct full multiaddr if we have the Peer ID
        let relayMultiaddr = `/ip4/${this.config.relayUrl}/tcp/4001`;
        if (relayPeerId) {
            relayMultiaddr += `/p2p/${relayPeerId}`;
        }

        const args = [
            '-relay', relayMultiaddr,
            '-file', absolutePath,
            '-port', this.config.seedPort.toString(),
            '-config', configPath
        ];

        console.log(`[Magnetk-JS] Spawning seeder: ${binPath} ${args.join(' ')}`);

        return new Promise((resolve, reject) => {
            const seeder = spawn(binPath, args);
            this.processes.push(seeder);

            let linkFound = false;

            seeder.stdout.on('data', (data) => {
                const output = data.toString();
                if (output.includes('Magnetk Link:')) {
                    const link = output.split('Magnetk Link:')[1].trim().split('\n')[0].trim();
                    linkFound = true;
                    this.emit('seeding', {
                        fileName: path.basename(absolutePath),
                        fileSize: fileStats.size,
                        hash: hash
                    });
                    resolve(link);
                }
            });

            seeder.stderr.on('data', (data) => {
                const msg = data.toString();
                console.log(`[Seeder Stderr] ${msg}`); // Uncommented for debug
                if (msg.includes('Connected to relay')) {
                    this.emit('relay-connected', { host: this.config.relayUrl, port: 4001 });
                }
            });

            seeder.on('error', (err) => {
                if (!linkFound) reject(err);
                this.emit('error', { code: 'SEEDER_ERROR', message: err.message });
            });

            seeder.on('close', (code) => {
                if (!linkFound && code !== 0) {
                    reject(new Error(`Seeder exited with code ${code}`));
                }
            });
        });
    }

    /**
     * Keeps seeding active processes.
     */
    async keepSeeding() {
        console.log('Press Ctrl+C to stop seeding');
        return new Promise(() => { }); // Wait forever
    }

    /**
     * Stops all active seeding processes.
     */
    stop() {
        this.processes.forEach(p => p.kill());
        this.processes = [];
    }

    /**
     * Downloads a file from a magnetk link.
     * @param {string} magnetURI 
     * @param {string} outputPath 
     */
    async download(magnetURI, outputPath) {
        const ml = typeof magnetURI === 'string' ? MagnetkLink.parse(magnetURI) : magnetURI;
        this.emit('download-start', { fileName: ml.fileName });

        // Resolve relay info from link or config
        const relayHost = ml.relayAddr ? ml.relayAddr.split('/')[2] : this.config.relayUrl;
        const discoveryPort = this.config.relayPort;

        let lookupResult;
        try {
            lookupResult = await this.lookupPeer(relayHost, discoveryPort, ml.fileHash);
        } catch (e) {
            this.emit('error', { code: 'LOOKUP_FAILED', message: e.message });
            throw e;
        }

        const addressesToTry = [];
        if (lookupResult && lookupResult.found && lookupResult.seeds.length > 0) {
            const seed = lookupResult.seeds[0];
            if (seed.transport_addr) addressesToTry.push(seed.transport_addr);
            if (seed.addresses) {
                seed.addresses.forEach(addr => {
                    const parts = addr.split('/');
                    if (parts.length >= 5 && parts[1] === 'ip4' && parts[3] === 'tcp') {
                        addressesToTry.push(`${parts[2]}:${parts[4]}`);
                    }
                });
            }
        }
        addressesToTry.push('127.0.0.1:4002');

        const uniqueAddrs = [...new Set(addressesToTry)];
        let socket;
        let connectionType = CONNECTION_TYPE.UNKNOWN;
        let finalAddr = null;

        for (const addr of uniqueAddrs) {
            const [host, portStr] = addr.split(':');
            const port = parseInt(portStr, 10) || 4002;

            try {
                socket = await this.connect(host, port);
                finalAddr = addr;
                if (host === '127.0.0.1' || host === 'localhost') connectionType = CONNECTION_TYPE.LOCAL;
                else if (host === relayHost) connectionType = CONNECTION_TYPE.RELAYED;
                else connectionType = CONNECTION_TYPE.DIRECT;

                this.emit('connection-type', { type: connectionType });
                break;
            } catch (e) { }
        }

        if (!socket) throw new Error('All seed connection attempts failed.');

        this.lastDownloadStats.connectionType = connectionType;
        this.lastDownloadStats.remoteAddr = finalAddr;

        const writer = new FrameWriter(socket);
        const reader = new FrameReader(socket);

        try {
            await writer.writeJSON(MSG_TYPE.MSG_MANIFEST_REQ, { file_hash: ml.fileHash });
            const manifestFrame = await reader.readFrame();
            const manifest = JSON.parse(manifestFrame.payload.toString());

            const fd = fs.openSync(outputPath, 'w');
            let bytesDownloaded = 0;
            const startTime = Date.now();

            for (let i = 0; i < manifest.total_chunks; i++) {
                await writer.writeJSON(MSG_TYPE.MSG_CHUNK_REQ, {
                    file_hash: ml.fileHash,
                    chunk_indices: [i]
                });

                const chunkFrame = await reader.readFrame();
                const data = chunkFrame.payload.slice(4);

                fs.writeSync(fd, data, 0, data.length, i * manifest.chunk_size);

                bytesDownloaded += data.length;
                const percent = (bytesDownloaded / manifest.file_size) * 100;
                const elapsedS = (Date.now() - startTime) / 1000;
                const speed = elapsedS > 0 ? (bytesDownloaded / 1024 / elapsedS).toFixed(1) : 0;

                this.emit('progress', {
                    percent,
                    bytesDownloaded,
                    totalBytes: manifest.file_size,
                    speed
                });
            }

            fs.closeSync(fd);
            this.emit('complete', { filePath: outputPath });
        } finally {
            socket.end();
        }
    }

    /**
     * Finds available seeders for a hash.
     */
    async lookupSeeds(fileHash) {
        const result = await this.lookupPeer(this.config.relayUrl, this.config.relayPort, fileHash);
        return result.found ? result.seeds : [];
    }

    _calculateHash(filePath) {
        return new Promise((resolve, reject) => {
            const hash = crypto.createHash('sha256');
            const stream = fs.createReadStream(filePath);
            stream.on('data', data => hash.update(data));
            stream.on('end', () => resolve(hash.digest('hex')));
            stream.on('error', reject);
        });
    }

    _resolveBinPath(binName) {
        const isWin = process.platform === 'win32';
        const isSeeder = binName.includes('seed');

        // 1. Check explicit config
        if (isSeeder && this.config.seederPath) return this.config.seederPath;

        // 2. Check environment variable
        const envVar = isSeeder ? 'MAGNETK_SEEDER_PATH' : null;
        if (envVar && process.env[envVar] && fs.existsSync(process.env[envVar])) {
            return process.env[envVar];
        }

        // 3. Check local dev environment paths
        const paths = [
            path.join(process.cwd(), 'bin', binName),
            path.join(process.cwd(), '..', '..', '..', 'bin', binName),
            path.join(process.cwd(), '..', '..', 'bin', binName),
            path.join(process.cwd(), '..', 'bin', binName)
        ];
        for (const p of paths) {
            if (fs.existsSync(p)) return p;
        }

        return binName; // Fallback to PATH (e.g. "seed" or "seed.exe")
    }

    _resolveConfigPath() {
        // 1. Check explicit config
        if (this.config.configPath) return this.config.configPath;

        // 2. Check local dev environment
        const paths = [
            path.join(process.cwd(), 'config', 'settings.conf'),
            path.join(process.cwd(), '..', '..', '..', 'config', 'settings.conf'),
            path.join(process.cwd(), '..', '..', 'config', 'settings.conf')
        ];
        for (const p of paths) {
            if (fs.existsSync(p)) return p;
        }
        return ''; // Let seeder use default
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
