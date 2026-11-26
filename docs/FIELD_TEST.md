# Field testing on real devices (Android app + PC host)

The field-test kit measures what Magnetk actually does on real networks —
latency, throughput, data integrity, security and behaviour when the phone
switches between Wi-Fi and mobile data — and sends **every log line and
result from the phone to the PC**, where they are printed, saved and
available over a local HTTP API.

```
 Android phone (Magnetk Test app)                 PC
 ┌──────────────────────────────┐          ┌──────────────────────────────────┐
 │ diag client (Go engine, JNI) │── relay ─│ magnetk diag host                │
 │ tests · monitor · net events │  :4002   │  embedded relay (or a VPS relay) │
 └──────────────────────────────┘          │  echo / sink / source / file     │
                                           │  log collector → diag-data/logs  │
                                           │  HTTP API 127.0.0.1:8090         │
                                           └──────────────────────────────────┘
```

## 1. Start the host on the PC

```bat
build\bin\magnetk.exe diag host -token <some-secret>
```

It prints the addresses to type into the phone, e.g.
`phone relay address: 192.168.1.67:4002`, and the relay's certificate
fingerprint. Options: `-listen` (embedded relay address, default
`0.0.0.0:4002`), `-direct` (peer-to-peer port, default `0.0.0.0:4012`),
`-file-mb` (shared test file, default 32), `-dir` (logs, default
`diag-data`), `-http` (API, default `127.0.0.1:8090`), `-v` (engine logs).

Windows Firewall must allow inbound TCP+UDP 4002 (and TCP+UDP 4012 for
peer-to-peer tests); accept the prompt the first time, or as administrator:

```bat
netsh advfirewall firewall add rule name="Magnetk 4002" dir=in action=allow protocol=TCP localport=4002
netsh advfirewall firewall add rule name="Magnetk 4002 UDP" dir=in action=allow protocol=UDP localport=4002
netsh advfirewall firewall add rule name="Magnetk 4012" dir=in action=allow protocol=TCP localport=4012
```

### Testing over mobile data

The phone on mobile data cannot reach a PC behind a home router, so use a
relay with a public address:

* **VPS relay** (recommended): run `magnetk relay -tokens <secret>` on the
  VPS ([RELAY_DEPLOYMENT.md](RELAY_DEPLOYMENT.md)), then on the PC either
  `magnetk diag host -listen "" -relay <vps>:4002 -token <secret>` (the
  host joins the VPS relay) or keep the embedded relay and federate it:
  `magnetk diag host -token <secret> -relay <vps>:4002 -secret <cluster-secret>`
  (start the VPS relay with the same `-secret`).
* In the app enter **both** relays, LAN first:
  `192.168.1.67:4002,<vps>:4002`. On Wi-Fi the phone uses the LAN relay; on
  mobile data the LAN address is skipped within 400 ms and the VPS is used.
* Or forward port 4002 (TCP+UDP) on your router to the PC and use your
  public IP.

## 2. Build and install the app

No Android Studio needed — only the Android SDK (build-tools, a platform
≥ 26, NDK), a JDK and Go:

```bat
python android\build.py --relay 192.168.1.67:4002 --token <some-secret>
```

→ `android\build\magnetk-test.apk` (arm64, ~3 MB; `--abis
arm64-v8a,armeabi-v7a,x86_64` for other devices/emulators; `--install`
installs over USB with adb). `--relay`/`--token` only pre-fill the fields.

Install: copy the APK to the phone and open it (allow "Install unknown
apps" for your file manager), or with USB debugging enabled:
`adb install -r android\build\magnetk-test.apk`.

## 3. Run the tests

In the app: check the relay/token fields → **Connect** → **Run all**.
Buttons: **Quick** (connect, ping, rpc, datagram), **Speed** (relayed and
peer-to-peer upload/download), **Monitor** (one ping per second; use it
while switching networks), **Loop x10**, **Stop**, **Share log**.

Everything also works remotely from the PC — useful while you hold the
phone and toggle networks:

```bash
curl "http://127.0.0.1:8090/clients"                                  # who is connected
curl "http://127.0.0.1:8090/cmd?client=all&op=run&tests=all"          # run everything
curl "http://127.0.0.1:8090/cmd?client=all&op=run&tests=ping,rpc&repeat=5"
curl "http://127.0.0.1:8090/cmd?client=all&op=monitor&on=1"           # 1 ping/s
curl "http://127.0.0.1:8090/cmd?client=all&op=reconnect"
curl "http://127.0.0.1:8090/logs?client=<id>&n=300"                    # live log
curl "http://127.0.0.1:8090/results?client=<id>"                       # results (JSON)
curl "http://127.0.0.1:8090/status"                                   # host + relay stats
```

Or type `run`, `run ping,rpc`, `monitor on`, `monitor off`, `stop`,
`info`, `reconnect` in the host console. Logs are saved as JSON lines in
`diag-data/logs/<client>.jsonl`; the phone also keeps
`Android/data/com.magnetk.diag/files/magnetk-diag.log`
(`adb pull /sdcard/Android/data/com.magnetk.diag/files/magnetk-diag.log`).
Lines produced while the phone was offline are buffered and delivered after
it reconnects, so the PC sees the whole story.

## 4. What each test measures

| Test | Measures | Passes when |
|---|---|---|
| `connect` | transport, TLS version and cipher, relay, public address, clock offset to the PC, certificate fingerprint vs the host's relay | TLS 1.3, fingerprint matches (TCP) |
| `ping` | 40 relay round trips: min/p50/p90/p99/max/jitter, loss | at least one answer |
| `rpc` | 100 × 64 B and 10 × 256 KB request/reply to the PC via the relay (content compared), 16 concurrent requesters | every reply identical |
| `messages` | 20 000 × 256 B direct messages: send rate, delivered rate, loss, ordering | none lost, none out of order |
| `pubsub` | PC publishes 5 000 messages to the phone's topic: rate, first-message latency | all received |
| `datagram` | 300 unreliable messages echoed by the PC: loss, RTT | at least one echo |
| `upload.relay` / `download.relay` | 16 MiB through the relay: MB/s, time to first byte, SHA-256 of the data | hash matches |
| `echo.relay` | full-duplex 8 MiB each way at once | echoed data identical |
| `upload.auto` / `download.auto` | same, peer-to-peer when the phone can reach the PC (`direct: true`) | hash matches |
| `file` | downloads the PC's shared test file (multi-stream, chunk-verified) | whole-file SHA-256 matches |
| `auth` | connects with a wrong token | the relay rejects it (warns if the relay is open) |
| `reconnect` (automatic) | after every disconnect: downtime, old/new public address, protocol | — |
| `monitor` (every 30 pings) | RTT summary and losses during the monitoring window | no loss |

Network changes are reported by Android (`ConnectivityManager`) and logged
with type, link bandwidth estimate, signal strength, interface and
addresses; the engine reconnects immediately instead of waiting for the
heartbeat. Expect a `reconnect` result of a few hundred milliseconds when
switching Wi-Fi ↔ mobile data.

## 5. Without a phone

The same suite runs anywhere:

```bash
magnetk diag client -relay 192.168.1.67:4002 -token <secret> -tests all -repeat 3
magnetk diag client -relay ... -tests none -monitor 2m      # just monitor
magnetk diag client -relay ... -wait                        # wait for host commands
```

## Notes

* The Android build currently uses the TCP+TLS 1.3 transport (MsQuic is not
  yet built for Android); `transport=quic` therefore fails on the phone and
  `auto` uses TCP. Everything else is identical.
* Throughput numbers from the Android **emulator** are limited by its
  user-mode network (~2–3 MB/s) and say nothing about real phones.
* On Windows, Go's `time.Now()` only advances every ~0.5 ms; Magnetk
  measures latency with `QueryPerformanceCounter` (`pkg/hrtime`), so
  sub-millisecond numbers are real.

## Chat and voice notes

Chat is end-to-end encrypted (the PC host, `magnetk chat` and the phone
exchange keys automatically). Each bubble shows encrypted or NOT encrypted
and who signed it ("verified", "new key ... saved", or a red KEY CHANGED or
INVALID SIGNATURE). Messages from strangers (anyone except the PC host and
people you wrote to) arrive as message requests with **Allow** and **Block**.
**Verify** shows the safety number to compare with the other person. See
[SECURITY.md](SECURITY.md).

The app's **Chat** tab sends text and voice notes (**Rec** / **Stop**) over
the same Magnetk connection. Each message shows "✓ PC got it · N ms" when
the receiver acknowledged it. Received voice notes show "sha256 ✓" and play
on tap. Quiet recordings are boosted automatically.

On the PC, the diag host prints chat lines (`CHAT <- a52-3cc9: hi`) and saves
voice notes to `diag-data/chat/`. Reply over Magnetk with
`magnetk chat -relay 127.0.0.1:4002 -token <token> <phone id> "text"`, or
`-audio file.wav` for a voice note (see [LINKS.md](LINKS.md)). The phone
replies to whoever wrote last; **To PC** switches back. **My address** shares
the phone's `magnetk://<id>@<relay>` address, and opening a `magnetk://` link
on the phone opens the chat with that node.

**Wi-Fi boost** (on by default) holds a low-latency Wi-Fi lock while the app is
open, which removes most power-saving latency spikes. **Wi-Fi info** logs
the band, link speed and signal so results can be explained.

