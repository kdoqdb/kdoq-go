package kdoq_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/kdoqdb/kdoq-go"
	"github.com/kdoqdb/kdoq-go/value"
)

// An account is stored as a document. The kdoq tag names a field, falls back to
// the json tag and then to the field name, and ",key" binds the document key.
type account struct {
	ID    kdoq.Key `kdoq:",key"`
	Name  string   `kdoq:"name"`
	Age   int64    `kdoq:"age"`
	Email string   `kdoq:"email,omitempty"`
}

// Connect with a DSN. A user name selects SCRAM-SHA-256; a server with a
// self-signed certificate is trusted by pinning the hash of its certificate.
func ExampleConnect() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	certificateDER := []byte("the server's DER certificate, delivered over a trusted channel")
	hash := sha256.Sum256(certificateDER)

	client, err := kdoq.Connect(ctx, "kdoq://ann@db.example.com:27505/shop", kdoq.ConnectOptions{
		Password:        "the password stays out of the DSN",
		CertificateHash: hash[:],
		OnState: func(state kdoq.State, err error) {
			log.Printf("connection is %v (%v)", state, err)
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close(context.Background())
}

// Read and write documents of a collection typed with a struct.
func ExampleCollection_Get() {
	ctx := context.Background()
	client := exampleClient()
	defer client.Close(ctx)

	db, err := client.DB("") // the database named in the DSN
	if err != nil {
		log.Fatal(err)
	}
	users := db.Collection[account]("users") // no network traffic

	if _, err := users.Put(ctx, "u1", account{Name: "Ann", Age: 31}); err != nil {
		log.Fatal(err)
	}
	ann, err := users.Get(ctx, "u1")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(ann.ID, ann.Name, ann.Age)
	// Output: u1 Ann 31
}

// Put replaces or creates the document under a key, and Delete removes it. A
// missing key is an error that matches ErrNotFound on Get, not on Delete.
func ExampleCollection_Delete() {
	ctx := context.Background()
	client := exampleClient()
	defer client.Close(ctx)
	db, _ := client.DB("")
	accounts := db.Collection[account]("accounts")

	if _, err := accounts.Put(ctx, "u1", account{Name: "Ann"}, kdoq.WriteOptions{Durability: kdoq.DurabilityDurable}); err != nil {
		log.Fatal(err)
	}
	if _, err := accounts.Delete(ctx, "u1"); err != nil {
		log.Fatal(err)
	}
	_, err := accounts.Get(ctx, "u1")
	fmt.Println(errors.Is(err, kdoq.ErrNotFound))
	// Output: true
}

// Insert lets the server generate a key when none is given; the result is the
// key that was stored.
func ExampleCollection_Insert() {
	ctx := context.Background()
	client := exampleClient()
	defer client.Close(ctx)
	db, _ := client.DB("")

	key, err := db.Collection[account]("users").Insert(ctx, account{Name: "Bob", Age: 40})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(key != "")
	// Output: true
}

// Range over a query. Leaving the loop cancels the query and closes the
// server cursor, so an early exit never leaks one. The filter selects the
// documents on a real server; the fake server returns every document.
func ExampleCollection_Find() {
	ctx := context.Background()
	client := exampleClient()
	defer client.Close(ctx)
	db, _ := client.DB("")
	users := db.Collection[account]("users")
	for i, name := range []string{"Ann", "Bob", "Cy", "Di"} {
		key := kdoq.Key(fmt.Sprintf("u%d", i))
		if _, err := users.Put(ctx, key, account{Name: name, Age: int64(30 + i)}); err != nil {
			log.Fatal(err)
		}
	}

	seen := 0
	for item, err := range users.Find(ctx, "age >= 30", kdoq.FindOptions{PageSize: 2}).All() {
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(item.Key, item.Value.Name)
		if seen++; seen == 2 {
			break
		}
	}
	// Output:
	// u0 Ann
	// u1 Bob
}

// Aggregate runs a pipeline whose result has a different type. A reduction
// groups the selected documents, and every item carries its grouping key.
func ExampleCollection_Aggregate() {
	ctx := context.Background()
	client := exampleGroupedClient() // answers the reduction below with its groups
	defer client.Close(ctx)
	db, _ := client.DB("")

	byColor := []kdoq.PipelineStage{kdoq.Reduce("color", "0", "$prev + 1")}
	for item, err := range db.Docs("items").Aggregate[int64](ctx, "", byColor).All() {
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(item.Group.Value, item.Value)
	}
	// Output:
	// red 3
	// blue 1
}

// Count is answered from metadata without scanning the documents. A count with
// a pipeline is an Aggregate with the Count stage.
func ExampleCollection_Count() {
	ctx := context.Background()
	client := exampleClient()
	defer client.Close(ctx)
	db, _ := client.DB("")
	docs := db.Docs("items")
	for _, key := range []kdoq.Key{"a", "b", "c"} {
		if _, err := docs.Put(ctx, key, value.Doc{"active": true}); err != nil {
			log.Fatal(err)
		}
	}

	n, err := docs.Count(ctx, "active")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(n)
	// Output: 3
}

// A manual cursor is advanced one page at a time, for example behind a "load
// more" button. Its ID can be passed to another connection of the same
// identity, which continues it with Client.AttachCursor.
func ExampleCursor() {
	ctx := context.Background()
	client := exampleClient()
	defer client.Close(ctx)
	db, _ := client.DB("")
	docs := db.Docs("items")
	for i := 0; i < 5; i++ {
		if _, err := docs.Put(ctx, kdoq.Key(fmt.Sprintf("k%d", i)), value.Doc{"n": int32(i)}); err != nil {
			log.Fatal(err)
		}
	}

	cursor, err := docs.FindCursor(ctx, "", kdoq.FindOptions{PageSize: 2})
	if err != nil {
		log.Fatal(err)
	}
	defer cursor.Close(ctx) // idempotent

	for {
		page, err := cursor.Next(ctx)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(len(page.Items), page.Done)
		if page.Done {
			break
		}
	}
	// Output:
	// 2 false
	// 2 false
	// 1 true
}

// Update changes a document on the server with ordered stages. A value can be
// an expression of the query language; fields given as a value.D are applied
// in the order written. An update of an absent key matches nothing, and an
// upsert creates the document.
func ExampleCollection_Update() {
	ctx := context.Background()
	client := exampleClient()
	defer client.Close(ctx)
	db, _ := client.DB("")
	docs := db.Docs("counters")
	if _, err := docs.Put(ctx, "visits", value.Doc{"n": int32(1)}); err != nil {
		log.Fatal(err)
	}

	result, err := docs.Update(ctx, kdoq.ByKey("visits"), []kdoq.Stage{
		kdoq.Set(value.D{
			{Key: "n", Value: kdoq.Expr("n + 1")},
			{Key: "seen", Value: true},
		}),
		kdoq.Unset("stale"),
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(result.Key)

	// Upsert reports whether it had to create the document.
	created, err := docs.Upsert(ctx, kdoq.ByKey("clicks"), []kdoq.Stage{kdoq.Set(value.Doc{"n": int32(1)})})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(created.Key, created.Inserted)
	// Output:
	// visits
	// clicks true
}

// Handle errors with errors.Is against the sentinels. The outcome of an error
// tells a retry loop whether the operation may have been applied.
func ExampleError() {
	ctx := context.Background()
	client := exampleClient()
	defer client.Close(ctx)
	db, _ := client.DB("")

	_, err := db.Docs("users").Get(ctx, "nobody")
	switch {
	case err == nil:
	case errors.Is(err, kdoq.ErrNotFound):
		fmt.Println("no such user")
	case errors.Is(err, context.DeadlineExceeded):
		fmt.Println("timed out")
	default:
		var failure *kdoq.Error
		if errors.As(err, &failure) {
			fmt.Println(failure.Code, failure.Outcome)
		}
	}
	// Output: no such user
}
