package daemon

import (
	"context"
	gotls "crypto/tls"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/scottwater/lewp/internal/proxy"
	"github.com/scottwater/lewp/internal/registry"
	localtls "github.com/scottwater/lewp/internal/tls"
)

type Config struct {
	RegistryPath    string
	HTTPListeners   []net.Listener
	HTTPSListeners  []net.Listener
	ManagedSuffixes []string
	TLSConfig       *gotls.Config
	CAPath          string
	CAKeyPath       string
	// RequestLog receives one line per proxied request. When nil it defaults to
	// os.Stdout, which launchd routes to the daemon's StandardOutPath log file.
	RequestLog io.Writer
}

func Serve(ctx context.Context, cfg Config) error {
	if err := os.MkdirAll(filepath.Dir(cfg.RegistryPath), 0o700); err != nil {
		return err
	}
	store, err := registry.Open(cfg.RegistryPath)
	if err != nil {
		return err
	}
	defer store.Close()
	handler := proxy.NewWithSuffixes(store, cfg.ManagedSuffixes)
	requestLog := cfg.RequestLog
	if requestLog == nil {
		requestLog = os.Stdout
	}
	handler.Logger = log.New(requestLog, "", log.LstdFlags|log.LUTC)
	tlsConfig := cfg.TLSConfig
	if tlsConfig == nil && len(cfg.HTTPSListeners) > 0 {
		var ca *localtls.CA
		var err error
		if cfg.CAPath != "" && cfg.CAKeyPath != "" {
			ca, err = localtls.EnsureCA(cfg.CAPath, cfg.CAKeyPath, localtls.CACommonName, []string{"lewp"})
		} else {
			ca, err = localtls.NewCA(localtls.CACommonName, []string{"lewp"})
		}
		if err != nil {
			return err
		}
		tlsConfig = localtls.NewManager(ca, []string{"lewp"}).TLSConfig()
	}

	var wg sync.WaitGroup
	var servers []*http.Server
	listenerCount := len(cfg.HTTPListeners) + len(cfg.HTTPSListeners)
	errs := make(chan error, listenerCount)
	serve := func(server *http.Server, ln net.Listener) {
		servers = append(servers, server)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := server.Serve(ln); err != nil && err != http.ErrServerClosed {
				errs <- err
			}
		}()
	}
	// newServer builds a proxy server with header-read and idle-connection
	// timeouts so a local client that opens a socket but never finishes its
	// request headers (or keeps an idle keep-alive connection open) cannot pin a
	// goroutine and fd forever. ReadTimeout/WriteTimeout are intentionally left
	// unset: the proxy carries arbitrarily long streaming responses (SSE, large
	// downloads) and slow request bodies (uploads), which a whole-request/response
	// deadline would truncate.
	newServer := func() *http.Server {
		return &http.Server{
			Handler:           handler,
			ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout:       120 * time.Second,
		}
	}
	for _, ln := range cfg.HTTPListeners {
		serve(newServer(), ln)
	}
	for _, ln := range cfg.HTTPSListeners {
		server := newServer()
		server.TLSConfig = tlsConfig
		serve(server, gotls.NewListener(ln, tlsConfig))
	}
	if len(servers) == 0 {
		<-ctx.Done()
		return nil
	}
	select {
	case err := <-errs:
		shutdownServers(servers)
		wg.Wait()
		return err
	case <-ctx.Done():
		shutdownServers(servers)
		wg.Wait()
		return nil
	}
}

func shutdownServers(servers []*http.Server) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, server := range servers {
		if err := server.Shutdown(ctx); err != nil {
			_ = server.Close()
		}
	}
}
