package com.magnetk.diag;

import android.app.Activity;
import android.graphics.Color;
import android.graphics.drawable.GradientDrawable;
import android.media.AudioFormat;
import android.media.AudioRecord;
import android.media.MediaPlayer;
import android.media.MediaRecorder;
import android.os.Handler;
import android.os.Looper;
import android.os.SystemClock;
import android.text.InputType;
import android.util.TypedValue;
import android.view.Gravity;
import android.view.View;
import android.view.ViewGroup;
import android.view.inputmethod.EditorInfo;
import android.widget.Button;
import android.widget.EditText;
import android.widget.LinearLayout;
import android.widget.ScrollView;
import android.widget.TextView;

import org.json.JSONArray;
import org.json.JSONObject;

import java.io.ByteArrayOutputStream;
import java.io.File;
import java.io.FileOutputStream;
import java.text.SimpleDateFormat;
import java.util.Date;
import java.util.HashMap;
import java.util.Locale;
import java.util.Map;

/**
 * Chat with the PC running "magnetk diag host": text and voice notes over the
 * Magnetk connection. Every message shows when the PC confirmed it and the
 * measured round trip; received voice notes show whether their SHA-256
 * matched and play on tap.
 */
final class ChatView extends LinearLayout {
    interface Host {
        boolean connected();

        /** Asks for the microphone permission; calls back startRecording() when granted. */
        boolean ensureMicPermission();

        void onUnread(int count);

        /** Node id of the PC host (the default recipient). */
        String hostId();

        /** Shares this phone's magnetk:// address. */
        void shareAddress();
    }

    private static final int RATE = 16000;
    private static final int MAX_RECORD_MS = 120_000;

    private final Activity act;
    private final Host host;
    private final Handler ui = new Handler(Looper.getMainLooper());
    private final LinearLayout list;
    private final ScrollView scroll;
    private final EditText input;
    private final Button recBtn;
    private final Map<String, TextView> metaById = new HashMap<>();
    private final Map<String, Double> earlyAcks = new HashMap<>(); // ack arrived before send() returned
    private final SimpleDateFormat hhmm = new SimpleDateFormat("HH:mm:ss", Locale.US);

    private volatile boolean recording;
    private MediaRecorder aacRec; // AAC voice notes (about 8x smaller than WAV)
    private File aacFile;
    private long recStart;
    private MediaPlayer player;
    private TextView playing;
    private boolean visible;
    private int unread;
    private String target = ""; // "" = the PC host
    private final TextView toView;

    ChatView(Activity act, Host host) {
        super(act);
        this.act = act;
        this.host = host;
        setOrientation(VERTICAL);

        LinearLayout top = new LinearLayout(act);
        top.setOrientation(HORIZONTAL);
        top.setGravity(Gravity.CENTER_VERTICAL);
        toView = new TextView(act);
        toView.setTextSize(TypedValue.COMPLEX_UNIT_SP, 13);
        top.addView(toView, new LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1));
        Button toPc = new Button(act);
        toPc.setText("To PC");
        toPc.setAllCaps(false);
        toPc.setOnClickListener(v -> setTarget(""));
        top.addView(toPc);
        Button verify = new Button(act);
        verify.setText("Verify");
        verify.setAllCaps(false);
        verify.setOnClickListener(v -> showSafety(target.isEmpty() ? host.hostId() : target));
        top.addView(verify);
        Button addr = new Button(act);
        addr.setText("My address");
        addr.setAllCaps(false);
        addr.setOnClickListener(v -> host.shareAddress());
        top.addView(addr);
        addView(top);
        setTarget("");

        list = new LinearLayout(act);
        list.setOrientation(VERTICAL);
        list.setPadding(0, dp(4), 0, dp(4));
        scroll = new ScrollView(act);
        scroll.addView(list);
        addView(scroll, new LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, 0, 1));

        LinearLayout bar = new LinearLayout(act);
        bar.setOrientation(HORIZONTAL);
        bar.setGravity(Gravity.CENTER_VERTICAL);
        input = new EditText(act);
        input.setHint("message to the PC");
        input.setTextSize(TypedValue.COMPLEX_UNIT_SP, 15);
        input.setInputType(InputType.TYPE_CLASS_TEXT | InputType.TYPE_TEXT_FLAG_CAP_SENTENCES | InputType.TYPE_TEXT_FLAG_MULTI_LINE);
        input.setMaxLines(4);
        input.setImeOptions(EditorInfo.IME_ACTION_SEND);
        input.setOnEditorActionListener((v, id, ev) -> {
            if (id == EditorInfo.IME_ACTION_SEND) {
                sendText();
                return true;
            }
            return false;
        });
        bar.addView(input, new LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1));
        Button send = new Button(act);
        send.setText("Send");
        send.setAllCaps(false);
        send.setOnClickListener(v -> sendText());
        bar.addView(send);
        recBtn = new Button(act);
        recBtn.setText("🎤 Rec");
        recBtn.setAllCaps(false);
        recBtn.setOnClickListener(v -> toggleRecording());
        bar.addView(recBtn);
        addView(bar);

        info("Chat with the PC. Text and voice notes go through Magnetk; ✓ shows the PC confirmed it, with the round trip.");
        startPoller();
    }

    private int dp(int v) {
        return (int) TypedValue.applyDimension(TypedValue.COMPLEX_UNIT_DIP, v, getResources().getDisplayMetrics());
    }

    void setShown(boolean shown) {
        visible = shown;
        if (shown) {
            unread = 0;
            host.onUnread(0);
            scrollDown();
        }
    }

    /** Sets the recipient ("" = the PC host). */
    void setTarget(String node) {
        if (node == null || node.equals(host.hostId())) node = "";
        target = node;
        toView.setText("To: " + (node.isEmpty() ? host.hostId() + " (PC)" : node));
    }

    /** Opens a chat from a magnetk:// link: the text is put in the box, never sent by itself. */
    void openLink(String node, String text) {
        setTarget(node);
        if (text != null && !text.isEmpty()) {
            input.setText(text);
            input.setSelection(text.length());
            info("from a magnetk:// link: check the message and tap Send");
        } else {
            info("chat with " + (target.isEmpty() ? host.hostId() : target) + " (from a magnetk:// link)");
        }
    }

    // ===== bubbles =====

    private void info(String text) {
        TextView t = new TextView(act);
        t.setText(text);
        t.setTextSize(TypedValue.COMPLEX_UNIT_SP, 12);
        t.setTextColor(Color.GRAY);
        t.setGravity(Gravity.CENTER_HORIZONTAL);
        t.setPadding(dp(8), dp(4), dp(8), dp(4));
        list.addView(t);
        scrollDown();
    }

    /** Adds a bubble; returns its meta line (time, delivery). */
    private TextView bubble(boolean out, String from, String body, final String audioPath) {
        LinearLayout wrap = new LinearLayout(act);
        wrap.setOrientation(VERTICAL);
        wrap.setGravity(out ? Gravity.END : Gravity.START);
        wrap.setPadding(out ? dp(48) : dp(4), dp(3), out ? dp(4) : dp(48), dp(3));

        LinearLayout b = new LinearLayout(act);
        b.setOrientation(VERTICAL);
        b.setPadding(dp(10), dp(6), dp(10), dp(6));
        GradientDrawable bg = new GradientDrawable();
        bg.setCornerRadius(dp(12));
        bg.setColor(out ? Color.rgb(214, 234, 255) : Color.rgb(236, 236, 236));
        b.setBackground(bg);

        if (!out) {
            TextView who = new TextView(act);
            who.setText(from);
            who.setTextSize(TypedValue.COMPLEX_UNIT_SP, 11);
            who.setTextColor(Color.rgb(30, 110, 60));
            b.addView(who);
        }
        TextView text = new TextView(act);
        text.setText(body);
        text.setTextSize(TypedValue.COMPLEX_UNIT_SP, 15);
        text.setTextColor(Color.rgb(20, 20, 20));
        text.setTextIsSelectable(audioPath == null);
        if (audioPath == null && body.contains("magnetk:")) {
            // magnetk:// addresses and magnetk:? links in messages are tappable
            android.text.util.Linkify.addLinks(text, java.util.regex.Pattern.compile("magnetk:[^\\s]+"), null);
        }
        b.addView(text);
        if (audioPath != null) {
            text.setTextColor(Color.rgb(20, 70, 160));
            b.setOnClickListener(v -> play(audioPath, text));
        }
        TextView meta = new TextView(act);
        meta.setTextSize(TypedValue.COMPLEX_UNIT_SP, 11);
        meta.setTextColor(Color.GRAY);
        meta.setText(hhmm.format(new Date()) + (out ? "  sending…" : ""));
        b.addView(meta);

        wrap.addView(b, new LayoutParams(ViewGroup.LayoutParams.WRAP_CONTENT, ViewGroup.LayoutParams.WRAP_CONTENT));
        list.addView(wrap);
        scrollDown();
        return meta;
    }

    private void scrollDown() {
        scroll.post(() -> scroll.fullScroll(View.FOCUS_DOWN));
    }

    private static String voiceLabel(int durMs, int size) {
        return String.format(Locale.US, "▶ Voice %.1fs · %s", durMs / 1000.0, size >= 1 << 20
                ? String.format(Locale.US, "%.1f MB", size / 1048576.0)
                : String.format(Locale.US, "%d KB", Math.max(1, size / 1024)));
    }

    // ===== sending =====

    private void sendText() {
        final String text = input.getText().toString().trim();
        if (text.isEmpty()) return;
        if (!host.connected()) {
            info("press Connect first");
            return;
        }
        input.setText("");
        final TextView meta = bubble(true, "", text, null);
        final String to = target;
        new Thread(() -> {
            String r = Native.chatSend(to, text);
            ui.post(() -> sent(meta, r));
        }).start();
    }

    private void sent(TextView meta, String result) {
        try {
            JSONObject o = new JSONObject(result);
            if (o.has("error")) {
                String err = o.getString("error");
                if (err.contains("not connected") && !target.isEmpty()) err = target + " is offline (tap To PC to write to the PC)";
                meta.setText(hhmm.format(new Date()) + "  ✗ " + err);
                meta.setTextColor(Color.RED);
                return;
            }
            JSONObject m = o.getJSONObject("msg");
            String id = m.getString("id");
            meta.setText(emoji(hhmm.format(new Date(m.getLong("t"))) + (m.optBoolean("encrypted") ? "  {e2e}" : "  {warn} not encrypted") + "  sent"));
            Double rtt = earlyAcks.remove(id);
            if (rtt != null) acked(meta, rtt);
            else metaById.put(id, meta);
        } catch (Exception e) {
            meta.setText("error: " + e);
        }
    }

    /** Describes who signed a message; returns true if it can be trusted. */
    private boolean trust(TextView meta, JSONObject m) {
        String t = m.optString("trust");
        String fp = m.optString("fingerprint");
        String shortFp = fp.length() >= 8 ? fp.substring(0, 4) + "-" + fp.substring(4, 8) : fp;
        String label;
        boolean ok = true;
        int color = Color.rgb(30, 130, 60);
        String enc = m.optBoolean("encrypted") ? "  {e2e} e2e" : "  {warn} NOT encrypted";
        meta.setText(meta.getText() + enc);
        if (!m.optString("error").isEmpty()) {
            meta.setText(meta.getText() + "  {warn} " + m.optString("error"));
            meta.setText(emoji(meta.getText().toString()));
            meta.setTextColor(Color.RED);
            return false;
        }
        switch (t) {
            case "verified":
                label = m.optBoolean("safety_checked") ? "  {lock} verified " + shortFp + " {check}{check} safety number" : "  {lock} verified " + shortFp;
                break;
            case "new":
                label = "  {key} new key " + shortFp + " saved";
                color = Color.rgb(160, 110, 0);
                break;
            case "changed":
                label = "  WARNING: KEY CHANGED - not the same sender as before!";
                color = Color.RED;
                ok = false;
                break;
            case "invalid":
                label = "  WARNING: INVALID SIGNATURE - forged or corrupted!";
                color = Color.RED;
                ok = false;
                break;
            case "unsigned":
                label = "  unsigned (old app)";
                color = Color.GRAY;
                break;
            default:
                return true;
        }
        meta.setText(emoji(meta.getText() + label));
        meta.setTextColor(color);
        return ok;
    }

    private static String emoji(String s) {
        return s.replace("{lock}", "\uD83D\uDD12").replace("{key}", "\uD83D\uDD11")
                .replace("{e2e}", "\uD83D\uDD10").replace("{warn}", "\u26A0").replace("{check}", "\u2713");
    }

    /** Shows the safety number with peer: both people compare it, then mark verified. */
    private void showSafety(final String peer) {
        if (!host.connected()) {
            info("press Connect first");
            return;
        }
        new Thread(() -> {
            final String num = Native.chatContact("safety", peer);
            ui.post(() -> {
                android.app.AlertDialog.Builder d = new android.app.AlertDialog.Builder(act).setTitle("Verify " + peer);
                if (num == null) {
                    d.setMessage("No key known for " + peer + " yet. Exchange a message first.").setPositiveButton("OK", null);
                } else {
                    TextView tv = new TextView(act);
                    tv.setText("Compare this safety number with what " + peer + " sees (read it aloud, or side by side). "
                            + "If it is the same, nobody is in the middle:" + "\n\n" + num.replace("  ", " ").replaceAll("(" + "\\d{5} " + "\\d{5} " + "\\d{5}) ", "$1\n"));
                    tv.setTypeface(android.graphics.Typeface.MONOSPACE);
                    tv.setTextSize(TypedValue.COMPLEX_UNIT_SP, 16);
                    tv.setPadding(dp(20), dp(10), dp(20), dp(4));
                    tv.setTextIsSelectable(true);
                    d.setView(tv)
                            .setPositiveButton("It matches: mark verified", (x, y) -> new Thread(() -> {
                                Native.chatContact("verified", peer);
                                ui.post(() -> info(peer + " is verified: messages show " + emoji("{check}{check}")));
                            }).start())
                            .setNegativeButton("Close", null);
                }
                d.show();
            });
        }).start();
    }

    /** Adds Allow / Block (or Accept new key / Block) under a message request. */
    private void requestButtons(TextView meta, final String from, boolean keyChanged) {
        final LinearLayout b = (LinearLayout) meta.getParent();
        final LinearLayout row = new LinearLayout(act);
        row.setOrientation(HORIZONTAL);
        TextView hint = new TextView(act);
        hint.setText(keyChanged ? "Key changed: accept only after checking with " + from : "Message request from " + from);
        hint.setTextSize(TypedValue.COMPLEX_UNIT_SP, 11);
        b.addView(hint);
        Button allow = new Button(act);
        allow.setText(keyChanged ? "Accept new key" : "Allow");
        allow.setAllCaps(false);
        allow.setOnClickListener(v -> new Thread(() -> {
            String err = keyChanged ? Native.chatContact("accept", from) : null;
            if (err == null) Native.chatContact("allow", from);
            final String e = err;
            ui.post(() -> {
                b.removeView(row);
                hint.setText(e != null ? "error: " + e : (keyChanged ? "new key accepted for " + from + ": tap Verify to compare the safety number" : from + " can message you now"));
                if (e == null) setTarget(from);
            });
        }).start());
        Button block = new Button(act);
        block.setText("Block");
        block.setAllCaps(false);
        block.setOnClickListener(v -> new Thread(() -> {
            Native.chatContact("block", from);
            ui.post(() -> {
                b.removeView(row);
                hint.setText(from + " is blocked");
            });
        }).start());
        row.addView(allow);
        row.addView(block);
        b.addView(row);
    }

    private void acked(TextView meta, double rttMs) {
        meta.setText(meta.getText().toString().replace("  sent", "")
                + String.format(Locale.US, "  ✓ PC got it · %.1f ms", rttMs));
        meta.setTextColor(Color.rgb(30, 130, 60));
    }

    // ===== receiving =====

    private void startPoller() {
        Thread t = new Thread(() -> {
            while (true) {
                String s = Native.chatPoll(1000);
                if (s == null) continue;
                ui.post(() -> handle(s));
            }
        }, "magnetk-chat");
        t.setDaemon(true);
        t.start();
    }

    private void handle(String json) {
        try {
            JSONArray arr = new JSONArray(json);
            for (int i = 0; i < arr.length(); i++) {
                JSONObject m = arr.getJSONObject(i);
                String kind = m.optString("kind");
                if (kind.equals("ack")) {
                    String id = m.optString("text");
                    String at = m.optString("trust");
                    if (at.equals("changed") || at.equals("invalid")) {
                        info("ignored an ack from " + m.optString("from") + ": " + (at.equals("changed") ? "its key changed" : "invalid signature"));
                        continue;
                    }
                    TextView meta = metaById.remove(id);
                    if (meta != null) acked(meta, m.optDouble("rtt_ms"));
                    else earlyAcks.put(id, m.optDouble("rtt_ms"));
                    continue;
                }
                String from = m.optString("from");
                boolean request = m.optBoolean("request");
                if (!request) setTarget(from); // reply goes to whoever wrote last
                TextView meta;
                if (kind.equals("file") && m.optBoolean("pending")) {
                    info("receiving " + m.optString("name") + " (" + voiceLabel(0, m.optInt("size")).replace("\u25B6 Voice 0.0s \u00B7 ", "") + ") from " + from + "\u2026");
                    continue;
                }
                if (kind.equals("file") && !m.optString("mime").startsWith("audio")) {
                    String cap = m.optString("text");
                    String label = "\uD83D\uDCCE " + m.optString("name") + " \u00B7 " + voiceLabel(0, m.optInt("size")).replace("\u25B6 Voice 0.0s \u00B7 ", "");
                    meta = bubble(false, from, cap.isEmpty() ? label : label + "\n" + cap, null);
                    boolean ok = m.optBoolean("verified");
                    meta.setText(meta.getText() + (ok ? "  sha256 \u2713 saved: " + m.optString("path") : "  " + m.optString("error", "SHA256 MISMATCH")));
                    if (!ok) meta.setTextColor(Color.RED);
                } else if (kind.equals("audio") || kind.equals("file")) {
                    String path = m.optString("path", null);
                    String label = voiceLabel(m.optInt("dur_ms"), m.optInt("size"));
                    String cap = m.optString("text");
                    meta = bubble(false, from, cap.isEmpty() ? label : label + "\n" + cap, path);
                    boolean ok = m.optBoolean("verified");
                    meta.setText(meta.getText() + (ok ? "  sha256 ✓" : "  SHA256 MISMATCH"));
                    if (!ok) meta.setTextColor(Color.RED);
                } else {
                    meta = bubble(false, from, m.optString("text"), null);
                }
                trust(meta, m);
                meta.setText(meta.getText() + String.format(Locale.US, "  · sent %s", hhmm.format(new Date(m.optLong("t")))));
                if (request) requestButtons(meta, from, m.optString("trust").equals("changed"));
                if (!visible) {
                    unread++;
                    host.onUnread(unread);
                }
            }
        } catch (Exception e) {
            info("chat error: " + e);
        }
    }

    // ===== voice notes =====

    private void toggleRecording() {
        if (recording) {
            if (aacRec != null) stopAac();
            else recording = false; // the WAV recorder thread finishes and sends
            return;
        }
        if (!host.connected()) {
            info("press Connect first");
            return;
        }
        if (host.ensureMicPermission()) startRecording();
    }

    /** Records an AAC (m4a) voice note; falls back to WAV if the device cannot. */
    void startRecording() {
        try {
            File dir = new File(act.getCacheDir(), "voice");
            dir.mkdirs();
            aacFile = new File(dir, "rec-" + System.currentTimeMillis() + ".m4a");
            MediaRecorder r = new MediaRecorder();
            r.setAudioSource(MediaRecorder.AudioSource.MIC);
            r.setOutputFormat(MediaRecorder.OutputFormat.MPEG_4);
            r.setAudioEncoder(MediaRecorder.AudioEncoder.AAC);
            r.setAudioChannels(1);
            r.setAudioSamplingRate(22050);
            r.setAudioEncodingBitRate(32000);
            r.setOutputFile(aacFile.getAbsolutePath());
            r.prepare();
            r.start();
            aacRec = r;
        } catch (Exception e) {
            aacRec = null;
            info("AAC recorder unavailable (" + e.getMessage() + "), recording WAV");
            startRecordingWav();
            return;
        }
        recording = true;
        recStart = SystemClock.elapsedRealtime();
        recBtn.setText("\u25A0 Stop");
        recBtn.setTextColor(Color.RED);
        tickRec();
    }

    private void stopAac() {
        final MediaRecorder r = aacRec;
        aacRec = null;
        recording = false;
        final int durMs = (int) (SystemClock.elapsedRealtime() - recStart);
        recBtn.setText("\uD83C\uDFA4 Rec");
        recBtn.setTextColor(Color.BLACK);
        boolean ok = true;
        try {
            r.stop();
        } catch (Exception e) {
            ok = false; // too short: nothing was recorded
        }
        r.release();
        if (!ok || durMs < 300) {
            aacFile.delete();
            info("voice note too short");
            return;
        }
        sendVoiceFile(aacFile, durMs, "audio/mp4");
    }

    /** Shows an outgoing voice bubble and sends the file (big ones go through the file engine). */
    private void sendVoiceFile(final File f, final int durMs, final String mime) {
        final TextView meta = bubble(true, "", voiceLabel(durMs, (int) f.length()), f.getAbsolutePath());
        final String to = target;
        new Thread(() -> {
            String r = Native.chatSendAudio(to, f.getAbsolutePath(), mime, durMs);
            ui.post(() -> sent(meta, r));
        }).start();
    }

    void startRecordingWav() {
        final int min = AudioRecord.getMinBufferSize(RATE, AudioFormat.CHANNEL_IN_MONO, AudioFormat.ENCODING_PCM_16BIT);
        final AudioRecord rec;
        try {
            rec = new AudioRecord(MediaRecorder.AudioSource.MIC, RATE, AudioFormat.CHANNEL_IN_MONO,
                    AudioFormat.ENCODING_PCM_16BIT, Math.max(min, RATE));
            if (rec.getState() != AudioRecord.STATE_INITIALIZED) throw new IllegalStateException("microphone busy");
        } catch (Exception e) {
            info("cannot record: " + e.getMessage());
            return;
        }
        recording = true;
        recStart = SystemClock.elapsedRealtime();
        recBtn.setText("■ Stop");
        recBtn.setTextColor(Color.RED);
        tickRec();
        new Thread(() -> {
            ByteArrayOutputStream pcm = new ByteArrayOutputStream();
            byte[] buf = new byte[Math.max(min, 3200)];
            rec.startRecording();
            while (recording && SystemClock.elapsedRealtime() - recStart < MAX_RECORD_MS) {
                int n = rec.read(buf, 0, buf.length);
                if (n > 0) pcm.write(buf, 0, n);
            }
            recording = false;
            rec.stop();
            rec.release();
            final byte[] data = pcm.toByteArray();
            final float gain = normalize(data);
            final int durMs = (int) (data.length * 1000L / (RATE * 2));
            ui.post(() -> {
                recBtn.setText("🎤 Rec");
                recBtn.setTextColor(Color.BLACK);
            });
            if (durMs < 300) {
                ui.post(() -> info("voice note too short"));
                return;
            }
            try {
                File dir = new File(act.getCacheDir(), "voice");
                dir.mkdirs();
                final File f = new File(dir, "rec-" + System.currentTimeMillis() + ".wav");
                writeWav(f, data);
                final TextView[] meta = new TextView[1];
                final Object lock = new Object();
                ui.post(() -> {
                    meta[0] = bubble(true, "", voiceLabel(durMs, (int) f.length())
                            + (gain > 1.05f ? String.format(Locale.US, " · gain x%.1f", gain) : ""), f.getAbsolutePath());
                    synchronized (lock) {
                        lock.notifyAll();
                    }
                });
                synchronized (lock) {
                    while (meta[0] == null) lock.wait();
                }
                String r = Native.chatSendAudio(target, f.getAbsolutePath(), "audio/wav", durMs);
                ui.post(() -> sent(meta[0], r));
            } catch (Exception e) {
                ui.post(() -> info("voice note failed: " + e));
            }
        }, "magnetk-rec").start();
    }

    private void tickRec() {
        if (!recording) return;
        long s = (SystemClock.elapsedRealtime() - recStart) / 1000;
        recBtn.setText(String.format(Locale.US, "■ Stop %d:%02d", s / 60, s % 60));
        ui.postDelayed(this::tickRec, 250);
    }

    /**
     * Raises quiet recordings (phone mics often record far below full scale)
     * so the peak reaches about 90%; at most x12. Returns the gain applied.
     */
    private static float normalize(byte[] pcm) {
        int peak = 1;
        for (int i = 0; i + 1 < pcm.length; i += 2) {
            int v = Math.abs((short) ((pcm[i] & 0xff) | (pcm[i + 1] << 8)));
            if (v > peak) peak = v;
        }
        float gain = Math.min(12f, 29500f / peak);
        if (gain <= 1.05f) return 1f;
        for (int i = 0; i + 1 < pcm.length; i += 2) {
            int v = Math.round((short) ((pcm[i] & 0xff) | (pcm[i + 1] << 8)) * gain);
            v = Math.max(-32768, Math.min(32767, v));
            pcm[i] = (byte) v;
            pcm[i + 1] = (byte) (v >> 8);
        }
        return gain;
    }

    private static void writeWav(File f, byte[] pcm) throws Exception {
        try (FileOutputStream o = new FileOutputStream(f)) {
            int n = pcm.length;
            o.write(new byte[]{'R', 'I', 'F', 'F'});
            le32(o, 36 + n);
            o.write(new byte[]{'W', 'A', 'V', 'E', 'f', 'm', 't', ' '});
            le32(o, 16);
            le16(o, 1); // PCM
            le16(o, 1); // mono
            le32(o, RATE);
            le32(o, RATE * 2);
            le16(o, 2);
            le16(o, 16);
            o.write(new byte[]{'d', 'a', 't', 'a'});
            le32(o, n);
            o.write(pcm);
        }
    }

    private static void le32(FileOutputStream o, int v) throws Exception {
        o.write(new byte[]{(byte) v, (byte) (v >> 8), (byte) (v >> 16), (byte) (v >> 24)});
    }

    private static void le16(FileOutputStream o, int v) throws Exception {
        o.write(new byte[]{(byte) v, (byte) (v >> 8)});
    }

    private void play(String path, TextView label) {
        if (path == null || path.isEmpty()) {
            info("voice note was not saved");
            return;
        }
        boolean same = label == playing;
        stopPlaying();
        if (same) return;
        try {
            player = new MediaPlayer();
            player.setDataSource(path);
            player.prepare();
            player.setOnCompletionListener(mp -> stopPlaying());
            player.start();
            playing = label;
            label.setText(label.getText().toString().replace("▶", "⏸"));
        } catch (Exception e) {
            stopPlaying();
            info("cannot play: " + e.getMessage());
        }
    }

    private void stopPlaying() {
        if (player != null) {
            try {
                player.release();
            } catch (Exception ignored) {
            }
            player = null;
        }
        if (playing != null) {
            playing.setText(playing.getText().toString().replace("⏸", "▶"));
            playing = null;
        }
    }

    void release() {
        recording = false;
        stopPlaying();
    }
}
