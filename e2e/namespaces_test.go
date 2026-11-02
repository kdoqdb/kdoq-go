//go:build e2e

package e2e_test

import (
	"context"
	"errors"
	"testing"

	"github.com/kdoqdb/kdoq-go"
	"github.com/kdoqdb/kdoq-go/value"
)

func TestDatabasesAndCollectionsHaveALifecycle(t *testing.T) {
	srv := startServer(t, settings{})
	client := srv.connect(t, "", "")
	ctx := opContext(t)
	dbs := client.Databases()

	if err := dbs.Create(ctx, "shop"); err != nil {
		t.Fatal(err)
	}
	if err := dbs.Create(ctx, "shop"); !errors.Is(err, kdoq.ErrAlreadyExists) {
		t.Fatalf("second Create = %v, want AlreadyExists", err)
	}
	list, err := dbs.List(ctx)
	if err != nil || len(list) != 1 || list[0].Name != "shop" || list[0].State != kdoq.ResourceLoaded {
		t.Fatalf("List = %+v, %v", list, err)
	}

	db, _ := client.DB("shop")
	if err := db.Collections().Create(ctx, "c"); err != nil {
		t.Fatal(err)
	}
	docs := db.Docs("c")
	if _, err := docs.Put(ctx, "k", value.Doc{"n": int32(1)}); err != nil {
		t.Fatal(err)
	}

	if err := docs.Unload(ctx); err != nil {
		t.Fatal(err)
	}
	info, err := docs.Info(ctx)
	if err != nil || info.State != kdoq.ResourceUnloaded {
		t.Fatalf("Info after Unload = %+v, %v", info, err)
	}
	if _, err := docs.Get(ctx, "k"); !errors.Is(err, kdoq.ErrUnavailable) {
		t.Fatalf("Get while unloaded = %v, want Unavailable", err)
	}
	if err := docs.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if got, err := docs.Get(ctx, "k"); err != nil || got["n"] != int32(1) {
		t.Fatalf("Get after Load = %v, %v", got, err)
	}

	if err := db.Unload(ctx); err != nil {
		t.Fatal(err)
	}
	if got, err := dbs.Get(ctx, "shop"); err != nil || got.State != kdoq.ResourceUnloaded {
		t.Fatalf("database after Unload = %+v, %v", got, err)
	}
	if err := db.Load(ctx); err != nil {
		t.Fatal(err)
	}
	cols, err := db.Collections().List(ctx)
	if err != nil || len(cols) != 1 || cols[0].Name != "c" {
		t.Fatalf("collections = %+v, %v", cols, err)
	}
}

func TestPersistenceSurvivesARestart(t *testing.T) {
	srv := startServer(t, settings{})
	client := srv.connect(t, "", "")
	docs := collection(t, client, "shop", "c")
	ctx := opContext(t)

	if _, err := docs.Put(ctx, "durable", value.Doc{"n": int32(1)}); err != nil {
		t.Fatal(err)
	}
	if _, err := docs.Put(ctx, "relaxed", value.Doc{"n": int32(2)}, kdoq.WriteOptions{Durability: kdoq.DurabilityRelaxed}); err != nil {
		t.Fatal(err)
	}
	if err := docs.WALSync(ctx); err != nil {
		t.Fatal(err)
	}
	if err := docs.Checkpoint(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := docs.Put(ctx, "after", value.Doc{"n": int32(3)}); err != nil {
		t.Fatal(err)
	}

	srv.restart()
	again := srv.connect(t, "", "")
	db, _ := again.DB("shop")
	for key, n := range map[kdoq.Key]int32{"durable": 1, "relaxed": 2, "after": 3} {
		got, err := db.Docs("c").Get(ctx, key)
		if err != nil || got["n"] != n {
			t.Fatalf("Get(%s) after restart = %v, %v", key, got, err)
		}
	}
}

func TestTheClientReconnectsAfterAServerRestart(t *testing.T) {
	srv := startServer(t, settings{})
	states := make(chan kdoq.State, 16)
	client := srv.connect(t, "", "", kdoq.ConnectOptions{OnState: func(s kdoq.State, _ error) { states <- s }})
	docs := collection(t, client, "shop", "c")
	ctx := opContext(t)
	if _, err := docs.Put(ctx, "k", value.Doc{"n": int32(1)}); err != nil {
		t.Fatal(err)
	}

	srv.restart()
	waitFor(t, states, kdoq.StateDisconnected)

	// A connection loss is never retried, so the first operation may fail; the
	// next one reconnects lazily.
	var got value.Doc
	var err error
	for range 2 {
		if got, err = docs.Get(ctx, "k"); err == nil {
			break
		}
		var connection *kdoq.ConnectionError
		var incomplete *kdoq.IncompleteError
		if !errors.As(err, &connection) && !errors.As(err, &incomplete) {
			t.Fatalf("Get after restart = %v", err)
		}
	}
	if err != nil || got["n"] != int32(1) {
		t.Fatalf("Get after reconnect = %v, %v", got, err)
	}
	waitFor(t, states, kdoq.StateConnected)
}

func waitFor(t *testing.T, states <-chan kdoq.State, want kdoq.State) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), eventually)
	defer cancel()
	for {
		select {
		case s := <-states:
			if s == want {
				return
			}
		case <-ctx.Done():
			t.Fatalf("timed out waiting for state %v", want)
		}
	}
}
