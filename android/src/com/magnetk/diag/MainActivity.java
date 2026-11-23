package com.magnetk.diag;

import android.app.Activity;
import android.content.Context;
import android.content.Intent;
import android.content.SharedPreferences;
import android.graphics.Color;
import android.graphics.Typeface;
import android.net.ConnectivityManager;
import android.net.LinkAddress;
import android.net.LinkProperties;
import android.net.Network;
import android.net.NetworkCapabilities;
import android.os.Build;
import android.os.Bundle;
import android.os.Handler;
import android.os.Looper;
import android.text.InputType;
import android.util.TypedValue;
import android.view.Gravity;
import android.view.View;
import android.view.ViewGroup;
import android.view.WindowManager;
import android.widget.Button;
import android.widget.EditText;
import android.widget.LinearLayout;
import android.widget.ScrollView;
import android.widget.TextView;

import org.json.JSONArray;
import org.json.JSONObject;

import java.util.ArrayDeque;
import java.util.Random;

/**
 * Magnetk field-test app: connects to a relay, runs the diagnostics suite
 * against the "magnetk diag host" running on a PC and streams every log line
 * and result to it. Network changes (Wi-Fi / mobile data) are reported to
 * the engine, which reconnects at once and measures the downtime.
 */
public class MainActivity extends Activity {
    private static final int MAX_LINES = 1500;

    private final Handler ui = new Handler(Looper.getMainLooper());
    private final ArrayDeque<String> lines = new ArrayDeque<>();
    private final StringBuilder logText = new StringBuilder();

    private EditText relayEdit, tokenEdit, idEdit, hostEdit, mbEdit, transportEdit;
    private TextView statusView, netView, logView;
    private ScrollView logScroll;
    private Button monitorBtn;
    private boolean monitoring, started, pollerRunning;
    private volatile String netDesc = "unknown";
    private Network lastNetwork;
    private String lastType = "";
    private long lastCapsLog;
    private ConnectivityManager.NetworkCallback netCallback;

    @Override
    protected void onCreate(Bundle saved) {
        super.onCreate(saved);
        getWindow().addFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON);
        setTitle("Magnetk field test");
        buildUi();
        startPoller();
        watchNetwork();
        ui.post(statusTick);
        append("Device: " + deviceInfo());
        append("Start 'magnetk diag host' on the PC, enter its address and press Connect.");
    }

    @Override
    protected void onDestroy() {
        ui.removeCallbacks(statusTick);
        if (netCallback != null) {
            try {
                cm().unregisterNetworkCallback(netCallback);
            } catch (Exception ignored) {
            }
        }
        pollerRunning = false;
        new Thread(Native::close).start();
        super.onDestroy();
    }

    // ===== UI =====

    private int dp(int v) {
        return (int) TypedValue.applyDimension(TypedValue.COMPLEX_UNIT_DIP, v, getResources().getDisplayMetrics());
    }

    private EditText field(LinearLayout parent, String label, String value, String hint, boolean number) {
        LinearLayout row = new LinearLayout(this);
        row.setOrientation(LinearLayout.HORIZONTAL);
        row.setGravity(Gravity.CENTER_VERTICAL);
        TextView l = new TextView(this);
        l.setText(label);
        l.setWidth(dp(84));
        row.addView(l);
        EditText e = new EditText(this);
        e.setSingleLine(true);
        e.setText(value);
        e.setHint(hint);
        e.setTextSize(TypedValue.COMPLEX_UNIT_SP, 14);
        e.setInputType(number ? InputType.TYPE_CLASS_NUMBER : InputType.TYPE_CLASS_TEXT | InputType.TYPE_TEXT_VARIATION_URI);
        row.addView(e, new LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1));
        parent.addView(row);
        return e;
    }

    private Button button(LinearLayout row, String text, View.OnClickListener l) {
        Button b = new Button(this);
        b.setText(text);
        b.setAllCaps(false);
        b.setTextSize(TypedValue.COMPLEX_UNIT_SP, 13);
        b.setOnClickListener(l);
        row.addView(b, new LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1));
        return b;
    }

    private LinearLayout row(LinearLayout parent) {
        LinearLayout r = new LinearLayout(this);
        r.setOrientation(LinearLayout.HORIZONTAL);
        parent.addView(r);
        return r;
    }

    private void buildUi() {
        SharedPreferences p = getPreferences(MODE_PRIVATE);
        String defId = "a52-" + Integer.toHexString(new Random().nextInt(0xffff));

        LinearLayout root = new LinearLayout(this);
        root.setOrientation(LinearLayout.VERTICAL);
        root.setPadding(dp(10), dp(6), dp(10), dp(6));

        statusView = new TextView(this);
        statusView.setTypeface(Typeface.DEFAULT_BOLD);
        statusView.setText("not started");
        root.addView(statusView);
        netView = new TextView(this);
        netView.setText("network: ?");
        root.addView(netView);

        relayEdit = field(root, "Relay", p.getString("relay", BuildConfig.DEFAULT_RELAY), "PC-IP:4002 or vps:4002 (comma = failover)", false);
        tokenEdit = field(root, "Token", p.getString("token", BuildConfig.DEFAULT_TOKEN), "relay token (optional)", false);
        idEdit = field(root, "Node id", p.getString("id", defId), "this phone's id", false);
        hostEdit = field(root, "PC host id", p.getString("host", "diag-host"), "diag-host", false);
        transportEdit = field(root, "Transport", p.getString("transport", "auto"), "auto | tcp", false);
        mbEdit = field(root, "Pipe MiB", p.getString("mb", "16"), "16", true);

        LinearLayout r1 = row(root);
        button(r1, "Connect", v -> connect());
        button(r1, "Run all", v -> run("all"));
        button(r1, "Quick", v -> run("connect,ping,rpc,datagram"));
        button(r1, "Speed", v -> run("upload.relay,download.relay,upload.auto,download.auto"));
        LinearLayout r2 = row(root);
        monitorBtn = button(r2, "Monitor", v -> toggleMonitor());
        button(r2, "Stop", v -> Native.stopTests());
        button(r2, "Loop x10", v -> runRepeat("all", 10));
        button(r2, "Share log", v -> shareLog());

        logView = new TextView(this);
        logView.setTypeface(Typeface.MONOSPACE);
        logView.setTextSize(TypedValue.COMPLEX_UNIT_SP, 10);
        logView.setTextIsSelectable(true);
        logView.setTextColor(Color.rgb(20, 20, 20));
        logScroll = new ScrollView(this);
        logScroll.addView(logView);
        root.addView(logScroll, new LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, 0, 1));
        setContentView(root);
    }

    private final Runnable statusTick = new Runnable() {
        @Override
        public void run() {
            if (started) {
                new Thread(() -> {
                    final String s = Native.status();
                    ui.post(() -> statusView.setText(s));
                }).start();
            }
            ui.postDelayed(this, 1000);
        }
    };

    private void append(String s) {
        for (String l : s.split("\n")) {
            lines.addLast(l);
        }
        while (lines.size() > MAX_LINES) {
            lines.removeFirst();
        }
        logText.setLength(0);
        for (String l : lines) {
            logText.append(l).append('\n');
        }
        boolean atBottom = !logScroll.canScrollVertically(1);
        logView.setText(logText);
        if (atBottom) {
            logScroll.post(() -> logScroll.fullScroll(View.FOCUS_DOWN));
        }
    }

    // ===== actions =====

    private void connect() {
        String relay = relayEdit.getText().toString().trim();
        if (relay.isEmpty()) {
            append("enter the relay address first (PC LAN IP:4002, or your VPS)");
            return;
        }
        getPreferences(MODE_PRIVATE).edit()
                .putString("relay", relay)
                .putString("token", tokenEdit.getText().toString().trim())
                .putString("id", idEdit.getText().toString().trim())
                .putString("host", hostEdit.getText().toString().trim())
                .putString("transport", transportEdit.getText().toString().trim())
                .putString("mb", mbEdit.getText().toString().trim())
                .apply();
        try {
            JSONObject cfg = new JSONObject();
            JSONArray relays = new JSONArray();
            for (String r : relay.split(",")) {
                if (!r.trim().isEmpty()) relays.put(r.trim());
            }
            cfg.put("relays", relays);
            cfg.put("token", tokenEdit.getText().toString().trim());
            cfg.put("id", idEdit.getText().toString().trim());
            cfg.put("host", hostEdit.getText().toString().trim());
            cfg.put("transport", transportEdit.getText().toString().trim());
            cfg.put("device", deviceInfo());
            cfg.put("network", netDesc);
            cfg.put("pipe_mb", parseInt(mbEdit.getText().toString(), 16));
            java.util.TimeZone tz = java.util.TimeZone.getDefault();
            cfg.put("tz_name", tz.getDisplayName(tz.inDaylightTime(new java.util.Date()), java.util.TimeZone.SHORT));
            cfg.put("tz_offset_sec", tz.getOffset(System.currentTimeMillis()) / 1000);
            cfg.put("cache_dir", getCacheDir().getAbsolutePath());
            java.io.File dir = getExternalFilesDir(null);
            cfg.put("data_dir", (dir != null ? dir : getFilesDir()).getAbsolutePath());
            final String json = cfg.toString();
            append("connecting to " + relay + " ...");
            new Thread(() -> {
                String err = Native.start(json);
                ui.post(() -> {
                    if (err != null) {
                        append("start failed: " + err);
                    } else {
                        started = true;
                        monitoring = false;
                        monitorBtn.setText("Monitor");
                    }
                });
            }).start();
        } catch (Exception e) {
            append("error: " + e);
        }
    }

    private void run(String tests) {
        runRepeat(tests, 1);
    }

    private void runRepeat(String tests, int repeat) {
        if (!started) {
            append("press Connect first");
            return;
        }
        Native.run(tests, repeat);
    }

    private void toggleMonitor() {
        if (!started) {
            append("press Connect first");
            return;
        }
        monitoring = !monitoring;
        Native.monitor(monitoring);
        monitorBtn.setText(monitoring ? "Monitor ON" : "Monitor");
    }

    private void shareLog() {
        Intent i = new Intent(Intent.ACTION_SEND);
        i.setType("text/plain");
        i.putExtra(Intent.EXTRA_SUBJECT, "Magnetk field test log");
        String text = logText.toString();
        if (text.length() > 400_000) text = text.substring(text.length() - 400_000);
        i.putExtra(Intent.EXTRA_TEXT, text);
        startActivity(Intent.createChooser(i, "Share log"));
    }

    private static int parseInt(String s, int def) {
        try {
            return Integer.parseInt(s.trim());
        } catch (Exception e) {
            return def;
        }
    }

    private void startPoller() {
        pollerRunning = true;
        Thread t = new Thread(() -> {
            while (pollerRunning) {
                final String s = Native.poll(500);
                if (s != null) ui.post(() -> append(s));
            }
        }, "magnetk-log");
        t.setDaemon(true);
        t.start();
    }

    // ===== network =====

    private ConnectivityManager cm() {
        return (ConnectivityManager) getSystemService(Context.CONNECTIVITY_SERVICE);
    }

    private static String typeOf(NetworkCapabilities c) {
        if (c == null) return "unknown";
        String t;
        if (c.hasTransport(NetworkCapabilities.TRANSPORT_WIFI)) t = "wifi";
        else if (c.hasTransport(NetworkCapabilities.TRANSPORT_CELLULAR)) t = "cellular";
        else if (c.hasTransport(NetworkCapabilities.TRANSPORT_ETHERNET)) t = "ethernet";
        else if (c.hasTransport(NetworkCapabilities.TRANSPORT_BLUETOOTH)) t = "bluetooth";
        else t = "other";
        if (c.hasTransport(NetworkCapabilities.TRANSPORT_VPN)) t = "vpn+" + t;
        return t;
    }

    private void watchNetwork() {
        netCallback = new ConnectivityManager.NetworkCallback() {
            @Override
            public void onCapabilitiesChanged(Network network, NetworkCapabilities caps) {
                String type = typeOf(caps);
                long now = System.currentTimeMillis();
                boolean changed = !network.equals(lastNetwork) || !type.equals(lastType);
                if (changed || now - lastCapsLog > 60_000) {
                    lastCapsLog = now;
                    StringBuilder sb = new StringBuilder("android: network ").append(type).append(" id=").append(network)
                            .append(" down=").append(caps.getLinkDownstreamBandwidthKbps()).append("kbps up=")
                            .append(caps.getLinkUpstreamBandwidthKbps()).append("kbps");
                    if (Build.VERSION.SDK_INT >= 29) sb.append(" signal=").append(caps.getSignalStrength());
                    sb.append(caps.hasCapability(NetworkCapabilities.NET_CAPABILITY_VALIDATED) ? " validated" : " not-validated");
                    sb.append(caps.hasCapability(NetworkCapabilities.NET_CAPABILITY_NOT_METERED) ? " unmetered" : " metered");
                    Native.log("INFO", sb.toString());
                }
                if (changed) {
                    lastNetwork = network;
                    lastType = type;
                    report(type + "#" + network);
                }
            }

            @Override
            public void onLinkPropertiesChanged(Network network, LinkProperties lp) {
                StringBuilder sb = new StringBuilder("android: link ").append(lp.getInterfaceName()).append(" addrs=");
                for (LinkAddress a : lp.getLinkAddresses()) sb.append(a).append(' ');
                sb.append("dns=").append(lp.getDnsServers());
                Native.log("INFO", sb.toString());
            }

            @Override
            public void onLost(Network network) {
                if (network.equals(lastNetwork)) {
                    lastNetwork = null;
                    lastType = "none";
                    report("none");
                }
            }
        };
        try {
            cm().registerDefaultNetworkCallback(netCallback, ui);
        } catch (Exception e) {
            append("network callback failed: " + e);
        }
    }

    private void report(final String desc) {
        netDesc = desc;
        netView.setText("network: " + desc);
        new Thread(() -> Native.network(desc)).start();
    }

    private static String deviceInfo() {
        return Build.MANUFACTURER + " " + Build.MODEL + " (" + Build.DEVICE + "), Android " + Build.VERSION.RELEASE
                + " API " + Build.VERSION.SDK_INT + ", " + (Build.SUPPORTED_ABIS.length > 0 ? Build.SUPPORTED_ABIS[0] : "?");
    }
}
