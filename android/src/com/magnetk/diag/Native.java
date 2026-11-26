package com.magnetk.diag;

/** JNI bindings to libmagnetkdiag.so (go/mobile). All calls are thread-safe. */
final class Native {
    static {
        System.loadLibrary("magnetkdiag");
    }

    private Native() {}

    /** Starts (or restarts) the test client; returns null or an error message. */
    static native String start(String configJson);

    /** Waits up to timeoutMs for new log lines; returns them newline-separated, or null. */
    static native String poll(int timeoutMs);

    /** Runs tests ("all" or comma separated names) in the background. */
    static native void run(String tests, int repeat);

    static native void stopTests();

    static native void monitor(boolean on);

    /** Reports the current network ("wifi", "cellular", "none", ...). */
    static native void network(String desc);

    /** Adds a line to the log that is sent to the PC. */
    static native void log(String level, String msg);

    static native String status();

    /** Sends a chat text to node "to" ("" = the PC host); returns {"msg":{...}} or {"error":"..."}. */
    static native String chatSend(String to, String text);

    /** Sends the voice note at path to node "to"; same result as chatSend. */
    static native String chatSendAudio(String to, String path, String mime, int durMs);

    /** This phone's identity: {"fingerprint":..,"formatted":..}, or null before Connect. */
    static native String identity();

    /** Pins the key a magnetk:// link promised for node; returns the old fingerprint if it differed. */
    static native String chatPin(String node, String fingerprint);

    /**
     * Contact action on peer: "allow", "block", "verified", "accept" (a changed key) or
     * "safety" (returns the safety number). Returns the safety number, an error, or null.
     */
    static native String chatContact(String action, String peer);

    /** Waits up to timeoutMs for chat events; returns a JSON array or null. */
    static native String chatPoll(int timeoutMs);

    static native void close();
}
