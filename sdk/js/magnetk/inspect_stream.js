import { createLibp2p } from 'libp2p';
import { tcp } from '@libp2p/tcp';
import { yamux } from '@libp2p/yamux';
import { noise } from '@libp2p/noise';
import { multiaddr } from '@multiformats/multiaddr';

async function run() {
    const node = await createLibp2p({
        transports: [tcp()],
        streamMuxers: [yamux()],
        connectionEncrypters: [noise()]
    });
    await node.start();

    const relayAddr = process.argv[2];
    if (!relayAddr) {
        console.error('Usage: node inspect_stream.js <relay_addr>');
        process.exit(1);
    }

    try {
        console.log('Dialing...');
        const stream = await node.dialProtocol(multiaddr(relayAddr), '/magnetk/discovery/1.0.0');

        console.log('Stream object keys:', Object.keys(stream));
        console.log('Stream prototype keys:', Object.keys(Object.getPrototypeOf(stream)));

        function getAllProperties(obj) {
            let props = [];
            do {
                props = props.concat(Object.getOwnPropertyNames(obj));
            } while (obj = Object.getPrototypeOf(obj));
            return props;
        }

        console.log('All properties including proto:', [...new Set(getAllProperties(stream))].filter(p => !p.startsWith('_')));

        console.log('stream.sink:', typeof stream.sink);
        console.log('stream.source:', typeof stream.source);
        console.log('stream[Symbol.asyncIterator]:', typeof stream[Symbol.asyncIterator]);

        await node.stop();
    } catch (err) {
        console.error('Error:', err);
        await node.stop();
    }
}

run();
