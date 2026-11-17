// HTTP spans from an uninstrumented Go server.
//
// Two uprobes:
//   net/http.serverHandler.ServeHTTP   entry  -> remember the start time
//   net/http.(*response).WriteHeader   entry  -> status code + elapsed time
//
// Correlation key is the OS thread id, which is the honest weakness: Go may
// migrate a goroutine between threads mid-call, so under heavy concurrency a
// duration can pair with the wrong request. Proper correlation needs a
// goroutine id, which means reading runtime internals (see docs/go-instrumentation.md).
// Every span produced here is therefore reported as completeness=partial.
#define SEC(name) __attribute__((section(name), used))
#define __uint(name, val) int(*name)[val]
#define __type(name, val) typeof(val) *name

typedef unsigned int u32;
typedef unsigned long long u64;

static u64 (*bpf_ktime_get_ns)(void) = (void *)5;
static u64 (*bpf_get_current_pid_tgid)(void) = (void *)14;
static long (*bpf_get_current_comm)(void *buf, u32 size_of_buf) = (void *)16;
static void *(*bpf_ringbuf_reserve)(void *ringbuf, u64 size, u64 flags) = (void *)131;
static void (*bpf_ringbuf_submit)(void *data, u64 flags) = (void *)132;
static void *(*bpf_map_lookup_elem)(void *map, const void *key) = (void *)1;
static long (*bpf_map_update_elem)(void *map, const void *key, const void *value, u64 flags) = (void *)2;
static long (*bpf_map_delete_elem)(void *map, const void *key) = (void *)3;

#define BPF_MAP_TYPE_RINGBUF 27
#define BPF_MAP_TYPE_LRU_HASH 9

struct pt_regs_min {
    u64 r15, r14, r13, r12, rbp, rbx;
    u64 r11, r10, r9, r8, rax, rcx;
    u64 rdx, rsi, rdi, orig_rax, rip, cs;
    u64 eflags, rsp, ss;
};

struct http_event {
    u32 pid;
    u32 tgid;
    u64 ts;
    u64 dur_ns;
    u32 status;
    u32 pad;
    char comm[16];
};

struct {
    __uint(type, BPF_MAP_TYPE_RINGBUF);
    __uint(max_entries, 1 << 20);
} http_events SEC(".maps");

// start time per OS thread; LRU so it can never grow without bound
struct {
    __uint(type, BPF_MAP_TYPE_LRU_HASH);
    __uint(max_entries, 8192);
    __type(key, u32);
    __type(value, u64);
} starts SEC(".maps");

SEC("uprobe/serve")
int on_serve(void *ctx)
{
    u32 tid = (u32)bpf_get_current_pid_tgid();
    u64 now = bpf_ktime_get_ns();
    bpf_map_update_elem(&starts, &tid, &now, 0);
    return 0;
}

SEC("uprobe/write_header")
int on_write_header(void *ctx)
{
    struct pt_regs_min *r = (struct pt_regs_min *)ctx;
    u32 tid = (u32)bpf_get_current_pid_tgid();
    u64 *start = bpf_map_lookup_elem(&starts, &tid);
    if (!start)
        return 0;

    u64 dur = bpf_ktime_get_ns() - *start;
    bpf_map_delete_elem(&starts, &tid);

    struct http_event *e = bpf_ringbuf_reserve(&http_events, sizeof(*e), 0);
    if (!e)
        return 0;

    u64 id = bpf_get_current_pid_tgid();
    e->pid = (u32)id;
    e->tgid = (u32)(id >> 32);
    e->ts = bpf_ktime_get_ns();
    e->dur_ns = dur;
    // WriteHeader(code int): receiver in AX, code in BX.
    e->status = (u32)r->rbx;
    e->pad = 0;
    bpf_get_current_comm(&e->comm, sizeof(e->comm));

    bpf_ringbuf_submit(e, 0);
    return 0;
}

char _license[] SEC("license") = "GPL";
