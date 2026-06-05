package lifecycle

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
)

// ShutdownHTTP drains an http.Server with DefaultTimeout and logs timeouts.
func ShutdownHTTP(logger *slog.Logger, name string, srv *http.Server) {
	if srv == nil {
		return
	}
	if logger == nil {
		logger = slog.Default()
	}
	ctx, cancel := context.WithTimeout(context.Background(), DefaultTimeout)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			logger.Warn(name+": shutdown timed out", "grace", DefaultTimeout)
			return
		}
		if !errors.Is(err, http.ErrServerClosed) {
			logger.Warn(name+": shutdown error", "err", err)
		}
	}
}
