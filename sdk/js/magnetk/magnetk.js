/**
 * Magnetk Link Parser and Generator for Magnetk P2P
 */
export class MagnetkLink {
    constructor(fileHash, fileName, fileSize, seedId, relayAddr) {
        this.fileHash = fileHash;
        this.fileName = fileName;
        this.fileSize = fileSize;
        this.seedId = seedId;
        this.relayAddr = relayAddr;
    }

    /**
     * Parses a magnetk URI string.
     * Format: magnetk:?xt=urn:sha256:<hash>&dn=<name>&xl=<size>&xs=<seed_id>&relay=<relay_addr>
     * @param {string} uri 
     * @returns {MagnetkLink}
     */
    static parse(uri) {
        if (!uri.startsWith('magnetk:?')) {
            throw new Error('Invalid magnet link: must start with magnetk:?');
        }

        const params = new URLSearchParams(uri.slice(9)); // slicing 'magnetk:?' length is 9? No. 'magnet:?' is 8. 'magnetk:?' is 9. Correct.
        const xt = params.get('xt');
        if (!xt || !xt.startsWith('urn:sha256:')) {
            throw new Error('Invalid magnet link: missing xt (urn:sha256:...)');
        }

        const fileHash = xt.split(':')[2];
        const fileName = params.get('dn') || 'unknown_file';
        const fileSize = parseInt(params.get('xl') || '0', 10);
        const seedId = params.get('xs') || '';
        const relayAddr = params.get('relay') || '';

        return new MagnetkLink(fileHash, fileName, fileSize, seedId, relayAddr);
    }

    /**
     * Generates a magnetk URI string.
     * @returns {string}
     */
    toString() {
        const parts = [];
        parts.push(`xt=urn:sha256:${this.fileHash}`);
        if (this.fileName) parts.push(`dn=${encodeURIComponent(this.fileName)}`);
        if (this.fileSize > 0) parts.push(`xl=${this.fileSize}`);
        if (this.seedId) parts.push(`xs=${encodeURIComponent(this.seedId)}`);
        if (this.relayAddr) parts.push(`relay=${this.relayAddr}`); // Multiaddr usually kept raw

        return `magnetk:?${parts.join('&')}`;
    }
}
