package kdoq_test

import (
	"context"
	"errors"
	"testing"

	"github.com/kdoqdb/kdoq-go"
	"github.com/kdoqdb/kdoq-go/internal/fakeserver"
)

func connectLevel(t *testing.T, level uint8, opts ...kdoq.ConnectOption) (*harness, *fakeserver.Server) {
	t.Helper()
	server := newServer(fakeserver.Config{Level: &level})
	return connect(t, server, "kdoq://h/shop", opts...), server
}

func TestDatabasesCreateListGetLoadAndUnload(t *testing.T) {
	h, server := connectDocs(t)
	ctx := context.Background()
	dbs := h.client.Databases()
	if err := dbs.Create(ctx, "shop"); err != nil {
		t.Fatal(err)
	}
	if err := dbs.Create(ctx, "archive"); err != nil {
		t.Fatal(err)
	}
	server.SetDatabaseState("archive", fakeserver.StateUnavailable, "recovery required")

	list, err := dbs.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []kdoq.DatabaseInfo{
		{Name: "archive", State: kdoq.ResourceUnavailable, Diagnostic: "recovery required"},
		{Name: "shop", State: kdoq.ResourceLoaded},
	}
	if len(list) != 2 || list[0] != want[0] || list[1] != want[1] {
		t.Fatalf("List = %+v, want %+v", list, want)
	}
	got, err := dbs.Get(ctx, "shop")
	if err != nil || got != want[1] {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	if err := dbs.Unload(ctx, "shop"); err != nil {
		t.Fatal(err)
	}
	if got, _ := dbs.Get(ctx, "shop"); got.State != kdoq.ResourceUnloaded {
		t.Fatalf("state after Unload = %q", got.State)
	}
	if err := dbs.Load(ctx, "shop"); err != nil {
		t.Fatal(err)
	}
	if got, _ := dbs.Get(ctx, "shop"); got.State != kdoq.ResourceLoaded {
		t.Fatalf("state after Load = %q", got.State)
	}
}

func TestDatabaseErrorsKeepTheirCodes(t *testing.T) {
	h, _ := connectDocs(t)
	ctx := context.Background()
	dbs := h.client.Databases()
	if _, err := dbs.Get(ctx, "missing"); !errors.Is(err, kdoq.ErrNotFound) {
		t.Fatalf("Get of a missing database = %v", err)
	}
	if err := dbs.Create(ctx, "shop"); err != nil {
		t.Fatal(err)
	}
	if err := dbs.Create(ctx, "shop"); !errors.Is(err, kdoq.ErrAlreadyExists) {
		t.Fatalf("second Create = %v", err)
	}
}

func TestDatabaseHandleMirrorsTheServiceMethods(t *testing.T) {
	h, _ := connectDocs(t)
	ctx := context.Background()
	db, err := h.client.DB("")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Create(ctx); err != nil {
		t.Fatal(err)
	}
	if info, err := db.Info(ctx); err != nil || info.Name != "shop" || info.State != kdoq.ResourceLoaded {
		t.Fatalf("Info = %+v, %v", info, err)
	}
	if err := db.Unload(ctx); err != nil {
		t.Fatal(err)
	}
	if info, _ := db.Info(ctx); info.State != kdoq.ResourceUnloaded {
		t.Fatalf("state after Unload = %q", info.State)
	}
	if err := db.Load(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestCollectionsListCreateLoadUnloadAndInfo(t *testing.T) {
	h, server := connectDocs(t)
	ctx := context.Background()
	db, _ := h.client.DB("")
	if err := db.Create(ctx); err != nil {
		t.Fatal(err)
	}
	cols := db.Collections()
	if err := cols.Create(ctx, "users"); err != nil {
		t.Fatal(err)
	}
	if err := cols.Create(ctx, "orders"); err != nil {
		t.Fatal(err)
	}
	server.SetCollectionFlags("shop", "orders", true, true)

	list, err := cols.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Name != "orders" || !list[0].LockReleased || !list[0].Excluded ||
		list[1].Name != "users" || list[1].LockReleased || list[1].State != kdoq.ResourceLoaded {
		t.Fatalf("List = %+v", list)
	}
	if err := cols.Unload(ctx, "users"); err != nil {
		t.Fatal(err)
	}
	info, err := db.Docs("users").Info(ctx)
	if err != nil || info.State != kdoq.ResourceUnloaded {
		t.Fatalf("Info after Unload = %+v, %v", info, err)
	}
	if err := db.Docs("users").Load(ctx); err != nil {
		t.Fatal(err)
	}
	if got, err := cols.Get(ctx, "users"); err != nil || got.State != kdoq.ResourceLoaded {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	if err := db.Docs("fresh").Create(ctx); err != nil {
		t.Fatal(err)
	}
	if err := cols.Create(ctx, "fresh"); !errors.Is(err, kdoq.ErrAlreadyExists) {
		t.Fatalf("duplicate Create = %v", err)
	}
	other, _ := h.client.DB("elsewhere")
	if _, err := other.Collections().List(ctx); !errors.Is(err, kdoq.ErrNotFound) {
		t.Fatalf("List of a missing database = %v", err)
	}
}

func TestWALSyncAndCheckpointReachTheNamedCollection(t *testing.T) {
	h, server := connectDocs(t)
	ctx := context.Background()
	c := h.docs("users")
	if err := c.WALSync(ctx); err != nil {
		t.Fatal(err)
	}
	if err := c.Checkpoint(ctx); err != nil {
		t.Fatal(err)
	}
	got := server.Persistence()
	want := []string{"wal_sync shop.users", "checkpoint shop.users"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("persistence calls = %v, want %v", got, want)
	}
}

func TestServerHealthAndCapabilities(t *testing.T) {
	h, _ := connectDocs(t)
	ctx := context.Background()
	ok, err := h.client.Server().Health(ctx)
	if err != nil || !ok {
		t.Fatalf("Health = %v, %v", ok, err)
	}
	caps, err := h.client.Server().Capabilities(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if caps.Encoding != 4 || caps.Capabilities != kdoq.CapAdministration|kdoq.CapDocuments|kdoq.CapQueries|kdoq.CapMutations {
		t.Fatalf("Capabilities = %+v", caps)
	}
	if len(caps.Operations) < 5 || caps.Operations[0] != 0x0001 {
		t.Fatalf("Operations = %v", caps.Operations)
	}
}

func TestSessionsListGetAndRevoke(t *testing.T) {
	server := newServer(fakeserver.Config{Users: map[string]string{"ann": "pw", "bob": "pw"}})
	ann := connect(t, server, "kdoq://ann:pw@h/shop")
	bob := connect(t, server, "kdoq://bob:pw@h/shop")
	ctx := context.Background()

	own, _, _ := ann.client.Session()
	list, err := ann.client.Sessions().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("an admin sees every session, got %+v", list)
	}
	got, err := ann.client.Sessions().Get(ctx, own.ID)
	if err != nil || got.ID != own.ID || got.Username != "ann" || got.Peer == "" || got.CreatedAt.IsZero() {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	bobSession, _, _ := bob.client.Session()
	if err := ann.client.Sessions().Revoke(ctx, bobSession.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := ann.client.Sessions().Get(ctx, bobSession.ID); !errors.Is(err, kdoq.ErrNotFound) {
		t.Fatalf("Get of a revoked session = %v", err)
	}
	if _, err := bob.client.Server().Health(ctx); !errors.Is(err, kdoq.ErrRevoked) {
		t.Fatalf("the revoked session's next operation = %v", err)
	}
}

func TestSessionsOutsideTheCallersScopeLookMissing(t *testing.T) {
	server := newServer(fakeserver.Config{Users: map[string]string{"ann": "pw", "bob": "pw"}, Level: ptr(uint8(0))})
	ann := connect(t, server, "kdoq://ann:pw@h/shop")
	bob := connect(t, server, "kdoq://bob:pw@h/shop")
	ctx := context.Background()
	list, err := ann.client.Sessions().List(ctx)
	if err != nil || len(list) != 1 || list[0].Username != "ann" {
		t.Fatalf("List = %+v, %v; below Admin only the caller's identity is visible", list, err)
	}
	other, _, _ := bob.client.Session()
	if _, err := ann.client.Sessions().Get(ctx, other.ID); !errors.Is(err, kdoq.ErrNotFound) {
		t.Fatalf("Get of a foreign session = %v, want NotFound", err)
	}
}

func ptr[T any](v T) *T { return &v }

func TestAdministrationAccessLevelFailuresAreForbidden(t *testing.T) {
	ctx := context.Background()
	t.Run("read level cannot administer", func(t *testing.T) {
		h, _ := connectLevel(t, 1)
		c := h.docs("users")
		for name, call := range map[string]func() error{
			"create database":   func() error { return h.client.Databases().Create(ctx, "x") },
			"unload collection": func() error { return c.Unload(ctx) },
			"wal sync":          func() error { return c.WALSync(ctx) },
			"checkpoint":        func() error { return c.Checkpoint(ctx) },
		} {
			if err := call(); !errors.Is(err, kdoq.ErrForbidden) {
				t.Fatalf("%s = %v, want Forbidden", name, err)
			}
		}
		if _, err := h.client.Databases().List(ctx); err != nil {
			t.Fatalf("a reader may list databases: %v", err)
		}
	})
	t.Run("connect level cannot read", func(t *testing.T) {
		h, _ := connectLevel(t, 0)
		if _, err := h.client.Databases().List(ctx); !errors.Is(err, kdoq.ErrForbidden) {
			t.Fatalf("List = %v, want Forbidden", err)
		}
		if ok, err := h.client.Server().Health(ctx); err != nil || !ok {
			t.Fatalf("Health at Connect level = %v, %v", ok, err)
		}
	})
}

func TestAdministrationNeedsTheNegotiatedCapability(t *testing.T) {
	server := newServer(fakeserver.Config{})
	h := connect(t, server, "kdoq://h/shop", kdoq.ConnectOptions{
		Capabilities: kdoq.Capabilities{Required: kdoq.CapDocuments | kdoq.CapQueries},
	})
	calls := 0
	server.Intercept = func(*fakeserver.Call) bool { calls++; return false }
	_, err := h.client.Server().Health(context.Background())
	if !errors.Is(err, kdoq.ErrUnsupported) || calls != 0 {
		t.Fatalf("Health = %v after %d server calls; want Unsupported before a stream opens", err, calls)
	}
}
