// gamejanitor-userns: enter a user namespace and exec the real binary.
//
// Go programs are multi-threaded by the time main() runs, so they cannot call
// unshare(CLONE_NEWUSER) — the kernel requires a single-threaded process.
// This helper runs before the Go runtime, creates the user namespace, sets up
// UID/GID mappings, and execs the gamejanitor binary inside it.
//
// Usage: gamejanitor-userns <binary> [args...]
//
// Build: musl-gcc -static -O2 -o userns-x86_64 userns.c

#define _GNU_SOURCE
#include <errno.h>
#include <fcntl.h>
#include <sched.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/types.h>
#include <unistd.h>

static int write_file(const char *path, const char *data) {
    int fd = open(path, O_WRONLY | O_TRUNC);
    if (fd < 0)
        return -1;
    ssize_t len = strlen(data);
    ssize_t written = write(fd, data, len);
    close(fd);
    return (written == len) ? 0 : -1;
}

int main(int argc, char **argv) {
    if (argc < 2) {
        fprintf(stderr, "usage: gamejanitor-userns <binary> [args...]\n");
        return 1;
    }

    uid_t uid = getuid();
    gid_t gid = getgid();

    // Already root — user namespace is unnecessary, just exec directly
    if (uid == 0) {
        execvp(argv[1], &argv[1]);
        fprintf(stderr, "exec %s: %s\n", argv[1], strerror(errno));
        return 1;
    }

    if (unshare(CLONE_NEWUSER) < 0) {
        fprintf(stderr, "unshare(CLONE_NEWUSER): %s\n", strerror(errno));
        if (errno == EPERM)
            fprintf(stderr,
                "hint: check that user namespaces are enabled "
                "(sysctl kernel.unprivileged_userns_clone=1)\n");
        return 1;
    }

    // Must deny setgroups before writing gid_map for unprivileged users
    if (write_file("/proc/self/setgroups", "deny") < 0) {
        fprintf(stderr, "write /proc/self/setgroups: %s\n", strerror(errno));
        return 1;
    }

    char buf[64];

    snprintf(buf, sizeof(buf), "0 %d 1\n", uid);
    if (write_file("/proc/self/uid_map", buf) < 0) {
        fprintf(stderr, "write /proc/self/uid_map: %s\n", strerror(errno));
        return 1;
    }

    snprintf(buf, sizeof(buf), "0 %d 1\n", gid);
    if (write_file("/proc/self/gid_map", buf) < 0) {
        fprintf(stderr, "write /proc/self/gid_map: %s\n", strerror(errno));
        return 1;
    }

    execvp(argv[1], &argv[1]);
    fprintf(stderr, "exec %s: %s\n", argv[1], strerror(errno));
    return 1;
}
