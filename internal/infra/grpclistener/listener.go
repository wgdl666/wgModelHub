// Package grpclistener 记下 gRPC 连接被对端掐断。
// 看图请求的 span 在 handler 返回时就结束，grpc 的 ConnEnd 又不带错误，复位只能在读或写上看到。
// 正常 EOF 不记，健康检查和对端正常关闭不会刷屏。
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

// ObserveResets 包住内网 gRPC 监听。公网 HTTP 不走这里，否则 net/http 拿不到 *net.TCPConn。
func ObserveResets(ln net.Listener) net.Listener {
	return resetListener{Listener: ln}
}

type resetListener struct{ net.Listener }

func (l resetListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &resetConn{
		Conn:   conn,
		remote: conn.RemoteAddr().String(),
		local:  conn.LocalAddr().String(),
		start:  time.Now(),
	}, nil
}

type resetConn struct {
	net.Conn
	remote string
	local  string
	start  time.Time
	once   sync.Once
}

func (c *resetConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.note("read", err)
	return n, err
}

func (c *resetConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	c.note("write", err)
	return n, err
}

func (c *resetConn) note(op string, err error) {
	if !isReset(err) {
		return
	}
	c.once.Do(func() {
		logs.Default().Error("grpc_conn_end",
			"remote_addr", c.remote,
			"local_addr", c.local,
			"duration_ms", time.Since(c.start).Milliseconds(),
			"op", op,
			"error", err.Error(),
		)
	})
}

func isReset(err error) bool {
	return errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, syscall.ECONNABORTED) ||
		errors.Is(err, io.ErrUnexpectedEOF)
}
