/**
 * Magnet Link Parser and Generator for Magnet P2P
 */
export class MagnetLink {
    constructor(fileHash, fileName, fileSize, seedId, relayAddr) {
        this.fileHash = fileHash;
        this.fileName = fileName;
        this.fileSize = fileSize;
        this.seedId = seedId;
        this.relayAddr = relayAddr;
    }

    /**
     * Parses a magnet URI string.
     * Format: magnet:?xt=urn:sha256:<hash>&dn=<name>&xl=<size>&xs=<seed_id>&relay=<relay_addr>
     * @param {string} uri 
     * @returns {MagnetLink}
     */
    static parse(uri) {
        if (!uri.startsWith('magnet:?')) {
            throw new Error('Invalid magnet link: must start with magnet:?');
        }

        const params = new URLSearchParams(uri.slice(8));
        const xt = params.get('xt');
        if (!xt || !xt.startsWith('urn:sha256:')) {
            throw new Error('Invalid magnet link: missing xt (urn:sha256:...)');
        }

        const fileHash = xt.split(':')[2];
        const fileName = params.get('dn') || 'unknown_file';
        const fileSize = parseInt(params.get('xl') || '0', 10);
        const seedId = params.get('xs') || '';
        const relayAddr = params.get('relay') || '';

        return new MagnetLink(fileHash, fileName, fileSize, seedId, relayAddr);
    }

    /**
     * Generates a magnet URI string.
     * @returns {string}
     */
    toString() {
        const parts = [];
        parts.push(`xt=urn:sha256:${this.fileHash}`);
        if (this.fileName) parts.push(`dn=${encodeURIComponent(this.fileName)}`);
        if (this.fileSize > 0) parts.push(`xl=${this.fileSize}`);
        if (this.seedId) parts.push(`xs=${encodeURIComponent(this.seedId)}`);
        if (this.relayAddr) parts.push(`relay=${this.relayAddr}`); // Multiaddr usually kept raw

        return `magnet:?${parts.join('&')}`;
    }
}
