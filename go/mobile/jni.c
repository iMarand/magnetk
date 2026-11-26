// JNI entry points for com.magnetk.diag.Native. Only compiled for Android.
//go:build android

#include <jni.h>
#include <stdlib.h>
#include "_cgo_export.h"

static jstring take(JNIEnv* env, char* s) {
    if (!s) return NULL;
    jstring r = (*env)->NewStringUTF(env, s);
    mkdFree(s);
    return r;
}

static char* dup(JNIEnv* env, jstring js) {
    if (!js) return NULL;
    const char* s = (*env)->GetStringUTFChars(env, js, NULL);
    if (!s) return NULL;
    size_t n = 0;
    while (s[n]) n++;
    char* out = (char*)malloc(n + 1);
    if (out) {
        for (size_t i = 0; i <= n; i++) out[i] = s[i];
    }
    (*env)->ReleaseStringUTFChars(env, js, s);
    return out;
}

#define CLS(name) Java_com_magnetk_diag_Native_##name

JNIEXPORT jstring JNICALL CLS(start)(JNIEnv* env, jclass cls, jstring cfg) {
    char* c = dup(env, cfg);
    char* r = mkdStart(c ? c : "{}");
    free(c);
    return take(env, r);
}

JNIEXPORT jstring JNICALL CLS(poll)(JNIEnv* env, jclass cls, jint timeoutMs) {
    return take(env, mkdPoll(timeoutMs));
}

JNIEXPORT void JNICALL CLS(run)(JNIEnv* env, jclass cls, jstring tests, jint repeat) {
    char* t = dup(env, tests);
    mkdRun(t ? t : "all", repeat);
    free(t);
}

JNIEXPORT void JNICALL CLS(stopTests)(JNIEnv* env, jclass cls) { mkdStopTests(); }

JNIEXPORT void JNICALL CLS(monitor)(JNIEnv* env, jclass cls, jboolean on) { mkdMonitor(on ? 1 : 0); }

JNIEXPORT void JNICALL CLS(network)(JNIEnv* env, jclass cls, jstring desc) {
    char* d = dup(env, desc);
    mkdNetwork(d ? d : "unknown");
    free(d);
}

JNIEXPORT void JNICALL CLS(log)(JNIEnv* env, jclass cls, jstring level, jstring msg) {
    char* l = dup(env, level);
    char* m = dup(env, msg);
    mkdLog(l ? l : "INFO", m ? m : "");
    free(l);
    free(m);
}

JNIEXPORT jstring JNICALL CLS(status)(JNIEnv* env, jclass cls) { return take(env, mkdStatus()); }

JNIEXPORT void JNICALL CLS(close)(JNIEnv* env, jclass cls) { mkdClose(); }

JNIEXPORT jstring JNICALL CLS(chatSend)(JNIEnv* env, jclass cls, jstring to, jstring text) {
    char* d = dup(env, to);
    char* t = dup(env, text);
    char* r = mkdChatSend(d ? d : "", t ? t : "");
    free(d);
    free(t);
    return take(env, r);
}

JNIEXPORT jstring JNICALL CLS(chatSendAudio)(JNIEnv* env, jclass cls, jstring to, jstring path, jstring mime, jint durMs) {
    char* d = dup(env, to);
    char* p = dup(env, path);
    char* m = dup(env, mime);
    char* r = mkdChatSendAudio(d ? d : "", p ? p : "", m ? m : "audio/wav", durMs);
    free(d);
    free(p);
    free(m);
    return take(env, r);
}

JNIEXPORT jstring JNICALL CLS(chatPoll)(JNIEnv* env, jclass cls, jint timeoutMs) {
    return take(env, mkdChatPoll(timeoutMs));
}

JNIEXPORT jstring JNICALL CLS(identity)(JNIEnv* env, jclass cls) { return take(env, mkdIdentity()); }

JNIEXPORT jstring JNICALL CLS(chatPin)(JNIEnv* env, jclass cls, jstring node, jstring fp) {
    char* n = dup(env, node);
    char* f = dup(env, fp);
    char* r = mkdChatPin(n ? n : "", f ? f : "");
    free(n);
    free(f);
    return take(env, r);
}

JNIEXPORT jstring JNICALL CLS(chatContact)(JNIEnv* env, jclass cls, jstring action, jstring peer) {
    char* a = dup(env, action);
    char* p = dup(env, peer);
    char* r = mkdChatContact(a ? a : "", p ? p : "");
    free(a);
    free(p);
    return take(env, r);
}
