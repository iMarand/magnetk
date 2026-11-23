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

    static native void close();
}
