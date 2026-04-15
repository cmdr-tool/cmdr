package daemon

import (
	"context"
	"net"
	"net/http"
)

// unixDialer returns an http.Transport DialContext that connects over a unix socket.
func unixDialer(sock string) *http.Transport {
	return &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return net.Dial("unix", sock)
		},
	}
}

// Client returns an HTTP client that talks to the daemon over its Unix socket.
func Client() *http.Client {
	return &http.Client{Transport: unixDialer(sockPath())}
}
