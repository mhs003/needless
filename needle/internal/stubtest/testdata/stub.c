/*
 * stub.c — a fake libneedle used to exercise the Go binding without the real
 * engine. It implements the exact C ABI from needle.h and can be told to
 * misbehave by the input string, so tests can cover error paths, truncation,
 * missing terminators and bogus return values deterministically.
 *
 * It is compiled to a shared library by internal/stubtest and dlopen'd by the
 * worker exactly like the production engine. Including needle.h means the
 * compiler checks every prototype against the real header.
 *
 * Behaviour switches (substring match on the completion/embed input):
 *
 *   FAIL_COMPLETE   -> needle_complete returns -13 and sets last_error
 *   TRUNCATE        -> fills the buffer, terminates the last byte
 *   NO_TERMINATOR   -> fills the buffer and omits the NUL entirely
 *   RETURN_BOGUS    -> writes "{}" but returns 999999
 *   NUL_EMBEDDED    -> writes "a\0b", so Go must cut at the first NUL
 *   UTF8            -> returns a JSON object containing multi-byte UTF-8
 *   FAIL_EMBED      -> needle_embed returns -1 and sets last_error
 *   INIT_FAIL       -> (in tools_json) needle_init returns -3
 *   INIT_BIG_PREFIX -> (in tools_json) needle_init returns 1000000
 */
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>

#include "needle.h"

static char g_error[512];
static int g_model_id;
static int g_resets;
static int g_inits;
static int g_loads;
static int g_completes;

static void set_error(const char *msg) {
    snprintf(g_error, sizeof(g_error), "%s", msg);
}

static const char *or_empty(const char *s) {
    return s ? s : "";
}

NEEDLE_API int needle_load(const unsigned char *data, unsigned long long size) {
    g_loads++;
    if (data == NULL) {
        set_error("stub: load: null data");
        return -1;
    }
    if (size < 6) {
        set_error("stub: load: truncated archive");
        return -1;
    }
    /* The .cact envelope starts with the little-endian magic 0x05E12A83. */
    if (!(data[0] == 0x83 && data[1] == 0x2A && data[2] == 0xE1 && data[3] == 0x05)) {
        set_error("stub: load: bad magic");
        return -1;
    }
    if (data[4] == 0) {
        set_error("stub: load: invalid model id");
        return -1;
    }
    g_model_id = data[4];
    return 0;
}

NEEDLE_API int needle_init(const char *system, const char *tools,
                           const char *tool_index_path) {
    g_inits++;
    (void)system;
    (void)tool_index_path;
    if (tools != NULL && strstr(tools, "INIT_FAIL") != NULL) {
        set_error("stub: init: prefix does not fit the context window");
        return -3;
    }
    if (tools != NULL && strstr(tools, "INIT_BIG_PREFIX") != NULL) {
        return 1000000;
    }
    return 7;
}

NEEDLE_API int needle_complete(const char *input, int max_new_tokens, char *out,
                               int out_capacity) {
    g_completes++;
    if (out == NULL || out_capacity <= 0) {
        set_error("stub: complete: no output buffer");
        return -1;
    }
    input = or_empty(input);

    if (strstr(input, "FAIL_COMPLETE") != NULL) {
        set_error("stub: complete: intentional failure");
        return -13;
    }
    if (strstr(input, "CRASH") != NULL) {
        /* Kill the worker process outright so tests can prove the parent
         * reports an unexpected exit instead of hanging. */
        abort();
    }
    if (strstr(input, "SLEEP") != NULL) {
        /* Block long enough for a context-cancellation test to fire. */
        struct timespec ts;
        ts.tv_sec = 2;
        ts.tv_nsec = 0;
        nanosleep(&ts, NULL);
    }
    if (strstr(input, "TRUNCATE") != NULL) {
        int i;
        for (i = 0; i < out_capacity - 1; i++) {
            out[i] = 'x';
        }
        out[out_capacity - 1] = '\0';
        return out_capacity - 1;
    }
    if (strstr(input, "NO_TERMINATOR") != NULL) {
        int i;
        for (i = 0; i < out_capacity; i++) {
            out[i] = 'y';
        }
        return out_capacity;
    }
    if (strstr(input, "RETURN_BOGUS") != NULL) {
        snprintf(out, (size_t)out_capacity, "{}");
        return 999999;
    }
    if (strstr(input, "NUL_EMBEDDED") != NULL) {
        /* Write "a\0b": Go must cut at the first NUL, yielding "a". */
        if (out_capacity > 3) {
            out[0] = 'a';
            out[1] = '\0';
            out[2] = 'b';
            out[3] = '\0';
            return 4;
        }
        out[0] = '\0';
        return 1;
    }
    if (strstr(input, "UTF8") != NULL) {
        snprintf(out, (size_t)out_capacity,
                 "{\"text\":\"h\xc3\xa9llo \xe4\xb8\x96\xe7\x95\x8c \xf0\x9f\x98\x80\"}");
        return (int)strlen(out);
    }

    snprintf(out, (size_t)out_capacity,
             "{\"type\":\"text\",\"model\":%d,\"resets\":%d,\"inits\":%d,"
             "\"completes\":%d,\"loads\":%d,\"max\":%d,\"input\":\"%s\"}",
             g_model_id, g_resets, g_inits, g_completes, g_loads,
             max_new_tokens, input);
    return (int)strlen(out);
}

NEEDLE_API int needle_embed(const char *input, float *out, int out_capacity) {
    if (out == NULL) {
        return 8; /* dimension query */
    }
    input = or_empty(input);
    if (strstr(input, "FAIL_EMBED") != NULL) {
        set_error("stub: embed: intentional failure");
        return -1;
    }
    if (out_capacity < 8) {
        set_error("stub: embed: buffer too small");
        return -8;
    }
    {
        size_t n = strlen(input);
        int i;
        for (i = 0; i < 8; i++) {
            out[i] = (float)(unsigned char)input[n ? (size_t)i % n : 0] + (float)i;
        }
    }
    return 8;
}

NEEDLE_API void needle_reset(void) {
    g_resets++;
}

NEEDLE_API const char *needle_last_error(void) {
    return g_error;
}
