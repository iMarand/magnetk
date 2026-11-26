// Types shared by the cgo files of the C API (see sdk/c/include/magnetk.h).
#ifndef MK_TYPES_H
#define MK_TYPES_H
#include <stdint.h>
#include <stdlib.h>

typedef struct mk_message {
    int32_t  kind;      // 1 direct, 2 publish, 3 request, 4 presence, 5 chat
    uint64_t id;
    char*    from;
    char*    subject;
    void*    data;
    int32_t  len;
    int32_t  datagram;
    uint64_t request;   // non-zero for requests: answer with mk_respond
} mk_message;
#endif
