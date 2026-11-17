// Minimal eBPF uprobe program: fires on a user-space function and emits one
// event per call through a ring buffer.
//
// Deliberately header-free. The only external requirement is clang with the bpf
// target, so the whole build runs in a container (Apple's clang cannot target
// bpf at all — see docs/agent-portability.md §5b). The two macros below are the
// BTF-style map definition that libbpf's bpf_helpers.h would normally provide.
#define SEC(name) __attribute__((section(name), used))
#define __uint(name, val) int(*name)[val]

typedef unsigned int u32;
typedef unsigned long long u64;

/* helper IDs from include/uapi/linux/bpf.h */
static u64 (*bpf_ktime_get_ns)(void) = (void *)5;
static u64 (*bpf_get_current_pid_tgid)(void) = (void *)14;
static long (*bpf_get_current_comm)(void *buf, u32 size_of_buf) = (void *)16;
static void *(*bpf_ringbuf_reserve)(void *ringbuf, u64 size, u64 flags) = (void *)131;
static void (*bpf_ringbuf_submit)(void *data, u64 flags) = (void *)132;

#define BPF_MAP_TYPE_RINGBUF 27

struct event {
    u32 pid;   /* thread id */
    u32 tgid;  /* process id */
    u64 ts;    /* monotonic nanoseconds */
    char comm[16];
};

struct {
    __uint(type, BPF_MAP_TYPE_RINGBUF);
    __uint(max_entries, 1 << 20);
} events SEC(".maps");

SEC("uprobe/fire")
int fire(void *ctx)
{
    struct event *e = bpf_ringbuf_reserve(&events, sizeof(*e), 0);
    if (!e)
        return 0;

    u64 id = bpf_get_current_pid_tgid();
    e->pid = (u32)id;
    e->tgid = (u32)(id >> 32);
    e->ts = bpf_ktime_get_ns();
    bpf_get_current_comm(&e->comm, sizeof(e->comm));

    bpf_ringbuf_submit(e, 0);
    return 0;
}

char _license[] SEC("license") = "GPL";
