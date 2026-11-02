//go:build e2e

package e2e_test

import (
	"errors"
	"testing"

	"github.com/kdoqdb/kdoq-go"
	"github.com/kdoqdb/kdoq-go/value"
)

var levels = settings{Users: []user{
	{Name: "root", Password: "pw-root"},
	{Name: "writer", Password: "pw-writer", Level: "write"},
	{Name: "reader", Password: "pw-reader", Level: "read"},
	{Name: "guest", Password: "pw-guest", Level: "connect"},
}}

func TestAccessLevelsLimitWhatAnIdentityMayDo(t *testing.T) {
	srv := startServer(t, levels)
	admin := srv.connect(t, "root", "pw-root")
	docs := collection(t, admin, "shop", "c")
	ctx := opContext(t)
	if _, err := docs.Put(ctx, "k", value.Doc{"n": int32(1)}); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		user, password string
		level          kdoq.AccessLevel
		read, write    bool
		admin          bool
	}{
		{"writer", "pw-writer", kdoq.AccessWrite, true, true, false},
		{"reader", "pw-reader", kdoq.AccessRead, true, false, false},
		{"guest", "pw-guest", kdoq.AccessConnect, false, false, false},
	}
	for _, c := range cases {
		t.Run(c.user, func(t *testing.T) {
			client := srv.connect(t, c.user, c.password)
			if _, level, _ := client.Session(); level != c.level {
				t.Fatalf("level = %v, want %v", level, c.level)
			}
			db, _ := client.DB("shop")
			handle := db.Docs("c")
			_, err := handle.Get(ctx, "k")
			expect(t, "read", err, c.read)
			_, err = handle.Put(ctx, "w", value.Doc{"n": int32(2)})
			expect(t, "write", err, c.write)
			err = db.Collections().Create(ctx, "other")
			expect(t, "administer", err, c.admin)
		})
	}
}

func expect(t *testing.T, what string, err error, allowed bool) {
	t.Helper()
	switch {
	case allowed && err != nil:
		t.Fatalf("%s: %v", what, err)
	case !allowed && !errors.Is(err, kdoq.ErrForbidden):
		t.Fatalf("%s = %v, want Forbidden", what, err)
	}
}

func TestSessionsAreListedAndRevoked(t *testing.T) {
	srv := startServer(t, levels)
	admin := srv.connect(t, "root", "pw-root")
	victim := srv.connect(t, "reader", "pw-reader")
	ctx := opContext(t)
	own, _, _ := victim.Session()

	list, err := admin.Sessions().List(ctx)
	if err != nil || len(list) < 2 {
		t.Fatalf("List = %+v, %v", list, err)
	}
	if err := admin.Sessions().Revoke(ctx, own.ID); err != nil {
		t.Fatal(err)
	}
	closedWithin(t, victim.Closed())
	if err := victim.Err(); !errors.Is(err, kdoq.ErrRevoked) {
		t.Fatalf("victim.Err = %v, want Revoked", err)
	}
}

func TestACursorContinuesFromASecondConnection(t *testing.T) {
	srv := startServer(t, levels)
	first := srv.connect(t, "reader", "pw-reader")
	second := srv.connect(t, "reader", "pw-reader")
	other := srv.connect(t, "writer", "pw-writer")
	docs := collection(t, srv.connect(t, "root", "pw-root"), "shop", "c")
	seed(t, docs, 5)
	ctx := opContext(t)

	db, _ := first.DB("shop")
	cur, err := db.Docs("c").FindCursor(ctx, "", kdoq.FindOptions{PageSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	head, err := cur.Next(ctx)
	if err != nil || len(head.Items) != 2 {
		t.Fatalf("first page = %+v, %v", head, err)
	}

	rest := 0
	attached := second.AttachCursor[value.Doc](cur.ID())
	for {
		page, err := attached.Next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		rest += len(page.Items)
		if page.Done {
			break
		}
	}
	if rest != 3 {
		t.Fatalf("the second connection read %d documents, want 3", rest)
	}

	// Another identity cannot continue a cursor it did not create.
	db, _ = first.DB("shop")
	fresh, err := db.Docs("c").FindCursor(ctx, "", kdoq.FindOptions{PageSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.AttachCursor[value.Doc](fresh.ID()).Next(ctx); !errors.Is(err, kdoq.ErrNotFound) {
		t.Fatalf("foreign attach = %v, want NotFound", err)
	}
	_ = fresh.Close(ctx)
}
