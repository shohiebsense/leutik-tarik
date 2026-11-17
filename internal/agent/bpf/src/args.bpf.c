// Reads argument registers at a user-space function entry.
//
// This is the piece that decides whether automatic HTTP capture is possible:
// Go 1.17+ passes arguments in registers (AX, BX, CX, DI, SI, R8, R9, R10, R11),
// not on the stack, so a probe must read them from pt_regs at entry.
//
// Header-free on purpose: only clang is needed to build it.
#define SEC(name) __attribute__((section(name), used))
#define __uint(name, val) int(*name)[val]

typedef unsigned int u32;
typedef unsigned long long u64;

static u64 (*bpf_ktime_get_ns)(void) = (void *)5;
static u64 (*bpf_get_current_pid_tgid)(void) = (void *)14;
static long (*bpf_get_current_comm)(void *buf, u32 size_of_buf) = (void *)16;
static void *(*bpf_ringbuf_reserve)(void *ringbuf, u64 size, u64 flags) = (void *)131;
static void (*bpf_ringbuf_submit)(void *data, u64 flags) = (void *)132;

#define BPF_MAP_TYPE_RINGBUF 27

// x86_64 struct pt_regs, field order from arch/x86/include/asm/ptrace.h.
struct pt_regs_min {
    u64 r15, r14, r13, r12, rbp, rbx;
    u64 r11, r10, r9, r8, rax, rcx;
    u64 rdx, rsi, rdi, orig_rax, rip, cs;
    u64 eflags, rsp, ss;
};

struct args_event {
    u32 pid;
    u32 tgid;
    u64 ts;
    u64 arg0; // Go ABI: AX
    u64 arg1; // BX
    u64 arg2; // CX
    char comm[16];
};

struct {
    __uint(type, BPF_MAP_TYPE_RINGBUF);
    __uint(max_entries, 1 << 20);
} arg_events SEC(".maps");

SEC("uprobe/args")
int args_probe(void *ctx)
{
    struct pt_regs_min *r = (struct pt_regs_min *)ctx;
    struct args_event *e = bpf_ringbuf_reserve(&arg_events, sizeof(*e), 0);
    if (!e)
        return 0;

    u64 id = bpf_get_current_pid_tgid();
    e->pid = (u32)id;
    e->tgid = (u32)(id >> 32);
    e->ts = bpf_ktime_get_ns();
    e->arg0 = r->rax;
    e->arg1 = r->rbx;
    e->arg2 = r->rcx;
    bpf_get_current_comm(&e->comm, sizeof(e->comm));

    bpf_ringbuf_submit(e, 0);
    return 0;
}

char _license[] SEC("license") = "GPL";
