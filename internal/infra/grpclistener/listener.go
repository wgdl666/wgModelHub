// Package grpclistener 记下 gRPC 连接被对端掐断。
// 看图请求的 span 在 handler 返回时就结束。grpc-go 的 stats.ConnEnd 不带错误，
// 传输层也只在详细级别 2 才打印 Closing。这里包住 Accept 出来的连接，记下第一次
// 读或写遇到的复位。正常 EOF 不记，健康检查和对端正常关闭不会刷屏。
package grpclistener

import (
	"errors"
	"io"
	"net"
	"sync"
	"syscall"
	"time"

	"github.com/wgdl666/kangaroo/logs"
)

// grpcDefaultKeepaliveTimeout 对齐 grpc 未配置 KeepaliveParams 时的默认超时。
// 包住连接后 grpc 认不出 *net.TCPConn，不会再设置 TCP_USER_TIMEOUT，交出去之前补上。
const grpcDefaultKeepaliveTimeout = 20 * time.Second

// ObserveResets 包住监听器。只用于内网 gRPC；公网 HTTP 仍要拿到 *net.TCPConn 才能开 keepalive。
func ObserveResets(ln net.Listener) net.Listener {
	return &loggingListener{Listener: ln}
}

type loggingListener struct {
	net.Listener
}

func (l *loggingListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		if tcp, ok := conn.(*net.TCPConn); ok {
			if err := setTCPUserTimeout(tcp, grpcDefaultKeepaliveTimeout); err != nil {
				_ = conn.Close()
				logs.Default().Error("grpc_tcp_user_timeout_failed", "remote_addr", conn.RemoteAddr().String(), "error", err.Error())
				continue
			}
		}
		return &loggedConn{
			Conn:   conn,
			remote: conn.RemoteAddr().String(),
			local:  conn.LocalAddr().String(),
			start:  time.Now(),
		}, nil
	}
}

type loggedConn struct {
	net.Conn
	remote string
	local  string
	start  time.Time
	once   sync.Once
}

func (c *loggedConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.note("read", err)
	return n, err
}

func (c *loggedConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	c.note("write", err)
	return n, err
}

func (c *loggedConn) SyscallConn() (syscall.RawConn, error) {
	sc, ok := c.Conn.(syscall.Conn)
	if !ok {
		return nil, errors.New("conn does not support syscall")
	}
	return sc.SyscallConn()
}

func (c *loggedConn) note(op string, err error) {
	if !terminalReset(err) {
		return
	}
	c.once.Do(func() {
		logs.Default().Error(
			"grpc_conn_end",
			"remote_addr", c.remote,
			"local_addr", c.local,
			"duration_ms", time.Since(c.start).Milliseconds(),
			"op", op,
			"error", err.Error(),
		)
	})
}

func terminalReset(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, syscall.ECONNABORTED) ||
		errors.Is(err, io.ErrUnexpectedEOF)
}
