# magnetk:// addresses, links and chat

Magnetk has two kinds of links:

| Form | Names | Example |
|---|---|---|
| `magnetk://node@relay[/action][?params]` | a node, a relay, or an action on a node | `magnetk://a52-3cc9@192.168.1.66:4002/chat?text=hi` |
| `magnetk:?xt=urn:sha256:...` | a file (content hash, size, providers, relays) | printed by `magnetk share` |

A link is only an **address**: it says *who* and *what*. The program that
opens it connects with the normal Magnetk protocol (TLS 1.3 over QUIC, or
over TCP when UDP is blocked) and does the work there. Nothing is fetched
from the link itself.

## Address format

```
magnetk://<node>@<relay host>[:port][/<action>][?relay=<more relays>&text=<message>&token=<relay token>]
```

| Part | Meaning |
|---|---|
| `node` | node id (`a52-3cc9`, `claude-pc`, `diag-host`) |
| `relay host:port` | a relay the node is connected to (port 4002 if omitted); more with `relay=` |
| `@` with no host | `magnetk://a52@/chat` uses the relays from your settings |
| `/chat` | open a chat (the default action for a node) |
| `text=` | message to put in the chat box. It is **never sent without a confirmation** |
| `token=` | relay token. Anyone who sees the link learns it, so share these links only privately |

`magnetk://relay.example.com:4002` (no node) names a relay. Opening it pings the relay.

## Chat from a terminal

```bash
magnetk chat -relay 192.168.1.66:4002 -token T a52-3cc9 "hello"        # send, wait for the ack
magnetk chat -relay 192.168.1.66:4002 -token T -audio note.wav a52-3cc9 # voice note
magnetk chat -relay 192.168.1.66:4002 -token T a52-3cc9                 # interactive
magnetk chat "magnetk://a52-3cc9@192.168.1.66:4002/chat?text=hi"        # from an address
```

In interactive mode you type lines; `/audio <file.wav> [caption]` sends a
voice note, `/to <node>` changes the recipient, and `/quit` exits. Received
voice notes are saved to `./magnetk-chat` (`-dir`) after their SHA-256 is
checked. Every message is acknowledged by the receiver, and the round trip
is printed (`✓ a52-3cc9 got it (round trip 7.0 ms)`).

Without `-id`, a chat session is named `<computer name>-<4 hex>`. The phone
app replies to whoever wrote last; **To PC** switches back to the PC host.

## Clicking links: `magnetk install`

`magnetk install` registers the `magnetk` scheme for the current user (no
admin rights needed). Clicking a link anywhere (browser, chat, QR scanner,
Run box) then runs `magnetk open -pause <link>` in a terminal window:

| OS | What is registered |
|---|---|
| Windows | `HKCU\Software\Classes\magnetk` → `"magnetk.exe" open -pause "%1"` |
| Linux / BSD | `~/.local/share/applications/magnetk-link.desktop` + `xdg-mime default … x-scheme-handler/magnetk` |
| macOS | `~/Applications/Magnetk Links.app` (AppleScript applet that receives the URL event and opens Terminal) |
| Android | the Magnetk app's intent filter (`<data android:scheme="magnetk"/>`) |

On a server without a desktop (a VPS relay or seed) it registers nothing,
because there is nothing to click there. `magnetk open '<link>'` works on any
machine. `magnetk install -n` prints what it would do; `-uninstall` removes it.

The install scripts do all of this:

```bash
scripts/install.sh                   # Linux/macOS desktop: PATH + links + ~/.magnetk/settings.conf
sudo scripts/install.sh --relay      # Linux server: /opt/magnetk + systemd relay service
sudo scripts/install.sh --seed /srv/files   # Linux server: seed sharing every file in a directory
powershell -ExecutionPolicy Bypass -File scripts\install.ps1           # Windows: PATH + links
powershell -ExecutionPolicy Bypass -File scripts\install.ps1 -Relay    # + firewall rule for 4002
```

`magnetk open` uses `./config/settings.conf` if it exists, otherwise
`~/.magnetk/settings.conf` (put `RELAYS` and `TOKEN` there), and downloads
file links to `~/Downloads/Magnetk`.

## Proving who you talk to (identity keys)

Every device has an Ed25519 **identity key** (`~/.magnetk/identity.key`, or
the app's `identity.key`, created on first use; `magnetk id` shows it).
Its **fingerprint** is 32 characters, e.g. `CERA TISM 5DSG PLIV UX3D 3JZI BWV6 4M2J`.

| Layer | What it stops |
|---|---|
| **Signed HELLO + name binding at the relay.** The node signs its id and the time; the relay binds the id to the first key that claims it (`relay-identities.json`). | Anyone else, even with the relay token, connecting as your id. They get `node id "x" belongs to another key`. |
| **Signed chat messages and acks.** The signature covers id, sender, recipient, text, attachment SHA-256 and time. | Messages forged or changed in transit, including by the relay itself. |
| **Known peers (trust on first use).** The receiver saves the sender's key the first time (`known-peers.json`), like SSH. | Someone else using that name later. Messages show **KEY CHANGED** in red. |
| **`key=` in addresses.** `magnetk://claude-pc@relay:4002?key=CERA...` | The first-contact gap: the link itself says which key must answer, like an HTTPS certificate but without a certificate authority. |

Labels you will see: **verified** (pinned key matches), **new key ... saved**
(first contact), **KEY CHANGED**, **INVALID SIGNATURE** (forged or corrupted),
and **unsigned** (old software).

For the strongest check, compare fingerprints once over another channel
(read them aloud, scan a QR code) or exchange `key=` addresses. On a relay,
`REQUIRE_IDENTITY=true` (`magnetk relay -require-identity`) refuses clients
without keys. Moving to a new device means copying `identity.key`, or using a
new id. The relay operator can release an id by deleting it from
`relay-identities.json`.

Chat is also **end-to-end encrypted**: the relay forwards ciphertext only.
Strangers arrive as message requests. See [SECURITY.md](SECURITY.md) for the
full model, including what someone who knows your `key=` can and cannot do.

## Safety rules

* A web page can open a `magnetk://` link without your consent, the same
  way it can open `mailto:`. So a link never sends anything by itself: the
  terminal asks `Send it? [y/N]`, and the phone app puts the text in the box
  for you to send.
* The link does not prove who wrote it. Who you are talking to is decided by
  the relay that authenticated the node (its token), not by the link.
* `token=` in a link is a secret. Prefer settings files for tokens.
