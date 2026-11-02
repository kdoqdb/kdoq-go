package kdoq_test

import (
	"context"
	"log"

	"github.com/kdoqdb/kdoq-go"
	"github.com/kdoqdb/kdoq-go/internal/fakeserver"
)

// exampleClient connects to an in-process fake server instead of a real one,
// so that the examples run under go test. The examples that go through it show
// the calls a program makes after Connect; ExampleConnect shows the connection
// itself. The fake stores documents, but it ignores filters and does not
// execute expressions: the examples say what a real server does.
func exampleClient(configure ...func(*fakeserver.Server)) *kdoq.Client {
	server := fakeserver.New(fakeserver.Config{})
	for _, c := range configure {
		c(server)
	}
	client, err := kdoq.Connect(context.Background(), "kdoq://localhost/shop", kdoq.WithTransport(server))
	if err != nil {
		log.Fatal(err)
	}
	return client
}

// exampleGroupedClient connects to a fake server that does not execute
// pipelines: it answers every query with the two groups a real server would
// return for the reduction of ExampleCollection_Aggregate.
func exampleGroupedClient() *kdoq.Client {
	return exampleClient(func(s *fakeserver.Server) {
		s.Query = func(*fakeserver.QueryRequest) fakeserver.QueryResult {
			return fakeserver.QueryResult{Items: []fakeserver.Result{
				{HasGroup: true, Group: "red", Value: int64(3)},
				{HasGroup: true, Group: "blue", Value: int64(1)},
			}}
		}
	})
}
