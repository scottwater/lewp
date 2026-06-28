package daemon

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/scottwater/lewp/internal/identity"
	"github.com/scottwater/lewp/internal/registry"
)

func TestServeRoutesHTTPSWithSNI(t *testing.T) {
	upstream := http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(r.Host))
	})}
	upstreamLn := listenLocal(t)
	go func() { _ = upstream.Serve(upstreamLn) }()
	t.Cleanup(func() { _ = upstream.Close() })

	registryPath := t.TempDir() + "/registry.sqlite"
	store, err := registry.Open(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	err = store.Remember(context.Background(), identity.Result{
		Root:           "audit",
		Name:           "feature-1",
		NormalizedRoot: "audit",
		NormalizedName: "feature-1",
		Host:           "feature-1.audit.lewp",
		HostKind:       identity.HostKindInstance,
		HostSource:     identity.SourceInferred,
		Path:           t.TempDir(),
		Kind:           identity.KindRoute,
	}, upstreamLn.Addr().(*net.TCPAddr).Port)
	if err != nil {
		t.Fatal(err)
	}
	_ = store.Close()

	httpsLn := listenLocal(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = Serve(ctx, Config{
			RegistryPath:   registryPath,
			HTTPSListeners: []net.Listener{httpsLn},
		})
	}()

	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true, ServerName: "feature-1.audit.lewp"}}}
	req, err := http.NewRequest(http.MethodGet, "https://"+httpsLn.Addr().String()+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "feature-1.audit.lewp"
	var resp *http.Response
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); {
		resp, err = client.Do(req)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "feature-1.audit.lewp") {
		t.Fatalf("body=%q", string(body))
	}
}

func listenLocal(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return ln
}
