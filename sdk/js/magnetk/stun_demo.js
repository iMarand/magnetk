import dgram from 'dgram';

/**
 * Magnetk STUN Demo
 * 
 * This script shows how to query a public STUN server to find your 
 * PUBLIC IP and PORT. This is the first step in "Hole Punching" 
 * for direct P2P file transfers.
 */

const STUN_SERVER = 'stun.l.google.com';
const STUN_PORT = 19302;

// STUN Binding Request Header (20 bytes)
// Structure: [Type (2 bytes)][Length (2 bytes)][Magic Cookie (4 bytes)][Transaction ID (12 bytes)]
const message = Buffer.alloc(20);
message.writeUInt16BE(0x0001, 0); // Binding Request
message.writeUInt16BE(0x0000, 2); // Message Length (0 attributes)
message.writeUInt32BE(0x2112A442, 4); // Magic Cookie

// Generate a random 12-byte Transaction ID
for (let i = 8; i < 20; i++) {
    message[i] = Math.floor(Math.random() * 256);
}

const socket = dgram.createSocket('udp4');

console.log(`--- Magnetk STUN Demo ---`);
console.log(`Querying STUN server: ${STUN_SERVER}:${STUN_PORT}...\n`);

socket.on('message', (msg) => {
    // Parse the response for the MAPPED-ADDRESS attribute (0x0001) or XOR-MAPPED-ADDRESS (0x0020)
    // For this simple demo, we just look for the magic cookie and then the IP data

    console.log('[STUN-Response] Received data from Google!');

    // Simplistic parsing: look for XOR-MAPPED-ADDRESS attribute
    let offset = 20;
    while (offset < msg.length) {
        const attrType = msg.readUInt16BE(offset);
        const attrLen = msg.readUInt16BE(offset + 2);

        if (attrType === 0x0020) { // XOR-MAPPED-ADDRESS
            const port = msg.readUInt16BE(offset + 6) ^ 0x2112; // XOR with Magic Cookie
            const ipParts = [];
            for (let i = 0; i < 4; i++) {
                ipParts.push(msg[offset + 8 + i] ^ message[4 + i]); // XOR with Magic Cookie
            }

            console.log('\n--- YOUR PUBLIC IDENTITY ---');
            console.log(`Public IP:   ${ipParts.join('.')}`);
            console.log(`Public Port: ${port}`);
            console.log('----------------------------');
            console.log('\n(This is the address you would tell your relay/relay so');
            console.log('other peers can talk to you DIRECTLY without a bridge.)\n');
            break;
        }
        offset += 4 + attrLen;
    }

    socket.close();
});

socket.on('error', (err) => {
    console.error(`Socket error: ${err.message}`);
    socket.close();
});

// Set a timeout
setTimeout(() => {
    console.log('STUN request timed out. (Are you offline?)');
    socket.close();
}, 5000);

socket.send(message, 0, message.length, STUN_PORT, STUN_SERVER);
