package runtime

import (
	"fmt"
	"os"
	"runtime"
	"syscall"
	"unsafe"
)

// CreateNetNS creates a new network namespace and bind-mounts it to the given
// path so it persists after this function returns. This is the same approach
// podman and other Go container tools use: lock a goroutine to an OS thread,
// unshare into a new netns, bind-mount it, then return to the original netns.
func CreateNetNS(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("creating netns bind target: %w", err)
	}
	f.Close()

	var nsErr error
	done := make(chan struct{})

	go func() {
		runtime.LockOSThread()
		// Don't unlock — the thread's netns state was modified.

		origNS, err := os.Open("/proc/self/ns/net")
		if err != nil {
			nsErr = fmt.Errorf("opening original netns: %w", err)
			close(done)
			return
		}
		defer origNS.Close()

		// Try net namespace only (works as root). Fall back to user+net
		// namespace (required for rootless — unprivileged CLONE_NEWNET
		// needs a user namespace).
		if err := syscall.Unshare(syscall.CLONE_NEWNET); err != nil {
			if err := syscall.Unshare(syscall.CLONE_NEWUSER | syscall.CLONE_NEWNET); err != nil {
				nsErr = fmt.Errorf("unshare CLONE_NEWNET: %w", err)
				close(done)
				return
			}
		}

		if err := syscall.Mount("/proc/self/ns/net", path, "", syscall.MS_BIND, ""); err != nil {
			setns(int(origNS.Fd()), syscall.CLONE_NEWNET)
			nsErr = fmt.Errorf("bind-mounting netns: %w", err)
			close(done)
			return
		}

		if err := setns(int(origNS.Fd()), syscall.CLONE_NEWNET); err != nil {
			nsErr = fmt.Errorf("restoring original netns: %w", err)
			close(done)
			return
		}

		close(done)
	}()

	<-done
	return nsErr
}

// DeleteNetNS unmounts and removes a bind-mounted network namespace file.
func DeleteNetNS(path string) {
	syscall.Unmount(path, syscall.MNT_DETACH)
	os.Remove(path)
}

func setns(fd int, nstype int) error {
	_, _, errno := syscall.RawSyscall(sysSetns, uintptr(fd), uintptr(nstype), 0)
	if errno != 0 {
		return errno
	}
	return nil
}

const sysSetns = 308 // SYS_SETNS on x86_64

// For other architectures, this would need to be adjusted.
// arm64: 268
func init() {
	// Verify we're on a supported architecture
	if unsafe.Sizeof(uintptr(0)) != 8 {
		panic("netns: unsupported architecture (need 64-bit)")
	}
}
