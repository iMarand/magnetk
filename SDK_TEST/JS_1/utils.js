export class MagnetkLink {
    constructor(fileHash, fileName, relayAddr = '', seedID = '') {
        this.fileHash = fileHash;
        this.fileName = fileName;
        this.relayAddr = relayAddr;
        this.seedID = seedID;
    }

    toString() {
        let link = `magnetk:?xt=urn:sha256:${this.fileHash}&dn=${encodeURIComponent(this.fileName)}`;
        if (this.relayAddr) {
            link += `&relay=${encodeURIComponent(this.relayAddr)}`;
        }
        if (this.seedID) {
            link += `&xs=${encodeURIComponent(this.seedID)}`;
        }
        return link;
    }

    static parse(linkStr) {
        if (!linkStr.startsWith('magnetk:?')) {
            throw new Error('Invalid magnet link: must start with magnetk:?');
        }

        const params = new URLSearchParams(linkStr.substring(9));
        const xt = params.get('xt');
        if (!xt || !xt.startsWith('urn:sha256:')) {
            throw new Error('Invalid magnet link: missing sha256 hash');
        }

        const fileHash = xt.substring(11);
        const fileName = params.get('dn') || '';
        const relayAddr = params.get('relay') || '';
        const seedID = params.get('xs') || '';

        return new MagnetkLink(fileHash, fileName, relayAddr, seedID);
    }
}
