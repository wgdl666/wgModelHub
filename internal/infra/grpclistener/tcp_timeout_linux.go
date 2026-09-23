//go:build linux

package grpclistener

import (
	"net"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func setTCPUserTimeout(conn *net.TCPConn, timeout time.Duration) error {
	rawConn, err := conn.SyscallConn()
	if err != nil {
		return err
	}
	var sockErr error
	if err := rawConn.Control(func(fd uintptr) {
		sockErr = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_TCP, unix.TCP_USER_TIMEOUT, int(timeout/time.Millisecond))
	}); err != nil {
		return err
	}
	return sockErr
}
