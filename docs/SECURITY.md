# Security: identities, end-to-end encryption, contacts

This page explains what Magnetk protects, how, and what it does not protect yet.

## In one paragraph

Every device has an **identity key**. Relays bind a node id to that key, so
nobody else can use your id. Chat messages and voice notes are **end-to-end
encrypted** for the recipient's key and **signed** by the sender's key. The
relay forwards ciphertext it cannot read or change. Receivers remember each
contact's key and warn loudly if it changes. Strangers arrive as **message
requests** until you allow them, and two people can compare a **safety
number** once to be sure nobody is in the middle.

## Layers

| Layer | Protects against | How |
|---|---|---|
| TLS 1.3 to the relay (QUIC or TCP) | Wi-Fi, ISP or network eavesdroppers | standard TLS on every hop |
| Relay token | strangers using your relay | `AUTH_TOKENS` / `-tokens` |
| Signed HELLO + **name binding** | anyone (even with the token) connecting as your id | Ed25519 signature over id + time; the relay binds the id to the first key (`relay-identities.json`) |
| **End-to-end encryption** | the relay, or anyone who gets into it, reading messages | X25519 + AES-256-GCM per message (below) |
| **Signatures** | forged or modified messages, fake acks | Ed25519 over every field and the attachment's SHA-256 |
| **Pinned keys** (trust on first use) | someone else answering as your contact later | `known-peers.json`; a different key is shown as **KEY CHANGED** and never accepted silently |
| `key=` in addresses | the first-contact gap | `magnetk://bob@relay:4002?key=<fingerprint>` says which key must answer |
| **Message requests** | spam and unknown senders | messages from non-contacts are held (no read receipt) until **Allow** |
| **Safety number** | a man in the middle on first contact | 60 digits, the same on both phones, compared once |

## Keys

* `identity.key` (created on first use; `magnetk id` shows it) holds an
  Ed25519 seed. The Ed25519 key signs. An X25519 key for encryption is
  derived from the same seed (HKDF-SHA256), so one file is the whole
  identity. **Keep it private**: the file is the identity. To move to a new
  device, copy it, or start with a new id (contacts will see KEY CHANGED).
* The fingerprint is the first 20 bytes of SHA-256(public key) in base32:
  `CERA TISM 5DSG PLIV UX3D 3JZI BWV6 4M2J`.

## Encryption of one message

```
outer header (visible to the relay): id, from, to, time, size, sender keys, ephemeral key, signature
ciphertext:                           text, kind, attachment type/length/hash, attachment bytes

e          = new X25519 key pair for this message
key, nonce = HKDF-SHA256( X25519(e, recipient) || X25519(sender, recipient),
                          salt = e.pub || sender.pub || recipient.pub, "magnetk-seal-v1" )
ciphertext = AES-256-GCM(key, nonce, inner message, aad = id|from|to|time)
signature  = Ed25519(sender, every header field including SHA-256(ciphertext))
```

**Files and long voice notes** (over 256 KB) are encrypted with a fresh
random key (AES-256-GCM in 1 MiB segments whose order and end are
authenticated) and shared through the file engine. Only the sealed message
carries the link, the key and the SHA-256. Anyone who obtains the link gets
ciphertext.

Before the first message, the two engines exchange signed key bundles
through a direct request. The relay routes it by node id, and the relay has
bound that id to its key. Old software without keys still works: messages to
it are signed but **not encrypted** and are labelled that way. With
`RequireEncryption` (`chat_require_encryption`) such messages are refused.

## Message requests, blocking, verification

| Action | App | CLI (`magnetk chat`) | Python | C++ | C |
|---|---|---|---|---|---|
| accept a stranger | **Allow** | `/allow bob` | `node.chat_allow("bob")` | `chat_allow` | `mk_chat_contact(n,"allow","bob",..)` |
| block | **Block** | `/block bob` | `chat_block` | `chat_block` | `"block"` |
| show safety number | **Verify** | `/safety bob` | `safety_number("bob")` | `safety_number` | `"safety"` |
| mark verified | **It matches** | `/verified bob` | `mark_verified` | `mark_verified` | `"verified"` |
| accept a changed key | **Accept new key** | `/accept bob` | `accept_new_key` | `accept_new_key` | `"accept"` |

Writing to someone makes them a contact. A held message is acknowledged
when you allow its sender.

## FAQ: "the link shows my key - can someone with it read my chats?"

No. The `key=` in a `magnetk://` address is the **public** fingerprint, like a
phone number with a built-in ID check. Knowing it lets people *verify* you,
not *be* you:

* To **send as you**, someone needs your private `identity.key`. The relay
  refuses your id with another key, and your contacts see KEY CHANGED.
* To **read** your messages, someone needs the recipient's private key.
  Messages are encrypted for that key alone, and the relay only sees
  ciphertext.
* There is no "joining" a one-to-one conversation. Each message is sealed
  for exactly one recipient. A third party, even with the relay token and
  both fingerprints, sees only that A sent B something of N bytes at time T.
* To stop strangers from even **messaging** you, keep message requests on
  (the default in the app and CLI) and block anyone unwanted.

The secret in a link is `token=` (relay access), if you put it there. It
lets someone use the relay, not read anything. Prefer giving tokens through
settings rather than links.

## What is not protected (yet)

* **Metadata**: the relay sees who talks to whom, when, and message sizes.
* **Forward secrecy**: each message uses a fresh ephemeral key, but there
  is no ratchet yet. Someone who records ciphertext *and later steals a
  device's identity key* can decrypt messages that were sent *to* that
  device. The Signal/WhatsApp Double Ratchet with rotating prekeys fixes
  this and is the next step.
* **First contact without verification** trusts the key it sees (like SSH).
  A `key=` address or a safety-number check removes that.
* **Groups** (one message to many) are not end-to-end encrypted yet. Pub/sub
  topics are protected by TLS only.
* Pub/sub, requests, pipes and file transfers are TLS-only. Files are still
  verified end to end by SHA-256 chunk hashes.

## Settings

| Setting / flag | Default | Meaning |
|---|---|---|
| `IDENTITY_FILE` / `-identity` | `~/.magnetk/identity.key` | node identity (`off` = none) |
| `IDENTITIES_FILE` / `relay -identities` | `relay-identities.json` | relay's id→key bindings |
| `REQUIRE_IDENTITY` / `relay -require-identity` | false | refuse nodes without keys |
| SDK `identity_file` | none | enable identity and secure chat |
| SDK `chat_require_approval` | false (app/CLI: on) | message requests |
| SDK `chat_require_encryption` | false | refuse unencrypted chat |
