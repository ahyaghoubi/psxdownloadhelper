package proxy

import (
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

// handleCONNECT bridges an opaque TCP tunnel between the console and the
// upstream host. The proxy design rules in docs/architecture.md forbid
// MITM: we never decrypt, never log payload bytes, never sniff. The
// console's HTTPS traffic (PSN auth, store) is forwarded as raw bytes.
func (s *Server) handleCONNECT(w http.ResponseWriter, r *http.Request) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijack not supported by ResponseWriter", http.StatusInternalServerError)
		return
	}

	upstream, err := net.DialTimeout("tcp", r.Host, 10*time.Second)
	if err != nil {
		s.logger.Warn("connect dial failed", "host", r.Host, "err", err)
		http.Error(w, "upstream dial: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer upstream.Close()

	clientConn, _, err := hj.Hijack()
	if err != nil {
		s.logger.Warn("hijack failed", "err", err)
		return
	}
	defer clientConn.Close()

	if _, err := clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		s.logger.Warn("connect ack failed", "err", err)
		return
	}

	// Bridge both directions; first error closes both pipes.
	var wg sync.WaitGroup
	wg.Add(2)
	closeBoth := func() {
		_ = clientConn.Close()
		_ = upstream.Close()
	}
	go func() {
		defer wg.Done()
		_, _ = io.Copy(upstream, clientConn)
		closeBoth()
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(clientConn, upstream)
		closeBoth()
	}()
	wg.Wait()
}
