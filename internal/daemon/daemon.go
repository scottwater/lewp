package daemon

import (
	"context"
	gotls "crypto/tls"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"github.com/scottwater/lewp/internal/proxy"
	"github.com/scottwater/lewp/internal/registry"
	localtls "github.com/scottwater/lewp/internal/tls"
)

type Config struct {
	RegistryPath   string
	HTTPListeners  []net.Listener
	HTTPSListeners []net.Listener
	TLSConfig      *gotls.Config
	CAPath         string
	CAKeyPath      string
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
	handler := proxy.New(store)
	tlsConfig := cfg.TLSConfig
	if tlsConfig == nil && len(cfg.HTTPSListeners) > 0 {
		var ca *localtls.CA
		var err error
		if cfg.CAPath != "" && cfg.CAKeyPath != "" {
			ca, err = localtls.EnsureCA(cfg.CAPath, cfg.CAKeyPath, "Lewp Local Development CA")
		} else {
			ca, err = localtls.NewCA("Lewp Local Development CA")
		}
		if err != nil {
			return err
		}
		tlsConfig = localtls.NewManager(ca).TLSConfig()
	}

	var wg sync.WaitGroup
	var servers []*http.Server
	errs := make(chan error, len(cfg.HTTPListeners)+len(cfg.HTTPSListeners))
	serve := func(server *http.Server, ln net.Listener) {
		servers = append(servers, server)
		wg.Add(1)
		go func() {
			defer wg.Done()
			// server.Serve returns http.ErrServerClosed when the server is
			// closed via server.Close below; that is a clean shutdown, not a
			// failure. Closing the listener directly would instead surface
			// net.ErrClosed, so we always close through the server.
			if err := server.Serve(ln); err != nil && err != http.ErrServerClosed {
				errs <- err
			}
		}()
	}
	for _, ln := range cfg.HTTPListeners {
		serve(&http.Server{Handler: handler}, ln)
	}
	for _, ln := range cfg.HTTPSListeners {
		serve(&http.Server{Handler: handler, TLSConfig: tlsConfig}, gotls.NewListener(ln, tlsConfig))
	}
	if len(servers) == 0 {
		<-ctx.Done()
		return nil
	}
	go func() {
		<-ctx.Done()
		for _, server := range servers {
			_ = server.Close()
		}
	}()
	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
		// Wait for the server goroutines to finish their clean shutdown so we
		// never return while listeners are still being torn down.
		wg.Wait()
		return nil
	}
}
