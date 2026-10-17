package registry

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/scottwater/lewp/internal/identity"
)

func TestMovePathRebasesStaticDirInsideProject(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	src := testIdentity(t, "feature.audit.lewp", identity.KindRoute)
	lease, err := store.Lease(ctx, src, PortRange{Start: 41100, End: 41110})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetRouteStaticDir(ctx, lease.RouteID, filepath.Join(src.Path, "dist")); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "renamed")

	moved, err := store.MovePath(ctx, src.Path, dest, identity.KindRoute)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dest, "dist")
	if len(moved) != 1 || moved[0].StaticDir != want {
		t.Fatalf("moved=%+v, want static dir %s", moved, want)
	}
	route, ok, err := store.RouteByHost(ctx, src.Host)
	if err != nil || !ok || route.StaticDir != want {
		t.Fatalf("RouteByHost = %+v, %v, %v; want static dir %s", route, ok, err, want)
	}
}

func TestRebaseStaticDir(t *testing.T) {
	for _, tc := range []struct{ dir, want string }{
		{"/work/app", "/work/renamed"},
		{"/work/app/dist", "/work/renamed/dist"},
		{"/work/app-other/dist", "/work/app-other/dist"},
		{"/elsewhere/dist", "/elsewhere/dist"},
		{"", ""},
	} {
		if got := rebaseStaticDir(tc.dir, "/work/app", "/work/renamed"); got != tc.want {
			t.Fatalf("rebaseStaticDir(%q) = %q, want %q", tc.dir, got, tc.want)
		}
	}
}
