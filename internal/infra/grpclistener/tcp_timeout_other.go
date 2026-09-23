//go:build !linux

package grpclistener

import (
	"net"
	"time"
)

func setTCPUserTimeout(*net.TCPConn, time.Duration) error { return nil }
