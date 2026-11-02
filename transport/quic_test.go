package transport_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/kdoqdb/kdoq-go/transport"
	"github.com/quic-go/quic-go"
)

func TestQUICControlProgressesWhileOperationWaitsForStreamCredit(t *testing.T) {
	certificateServer, trust := h2Server(t, func(http.ResponseWriter, *http.Request) {})
	tls := certificateServer.TLS.Clone()
	tls.NextProtos = []string{"kdoq-native"}
	listener, err := quic.ListenAddr("127.0.0.1:0", tls, &quic.Config{MaxIncomingStreams: 2, MaxIncomingUniStreams: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	tr, err := transport.NewQUIC(transport.QUICConfig{TLS: trust})
	if err != nil {
		t.Fatal(err)
	}
	client, err := tr.Dial(ctx, listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	peer, err := listener.Accept(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer peer.CloseWithError(0, "")
	control, err := client.OpenControl(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = control.Write([]byte("a")); err != nil {
		t.Fatal(err)
	}
	peerControl, err := peer.AcceptStream(ctx)
	if err != nil {
		t.Fatal(err)
	}
	echoDone := make(chan struct{})
	go func() { _, _ = io.Copy(peerControl, peerControl); close(echoDone) }()
	operation, err := client.OpenOperation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer operation.Abort()
	if _, err = operation.Write([]byte("hold")); err != nil {
		t.Fatal(err)
	}
	if _, err = peer.AcceptStream(ctx); err != nil {
		t.Fatal(err)
	}
	waiting, cancel := context.WithCancel(ctx)
	opened := make(chan error, 1)
	go func() {
		st, err := client.OpenOperation(waiting)
		if st != nil {
			_ = st.Abort()
		}
		opened <- err
	}()
	if _, err = control.Write([]byte("b")); err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 2)
	if _, err = io.ReadFull(control, data); err != nil || string(data) != "ab" {
		t.Fatalf("control = %q, %v", data, err)
	}
	cancel()
	select {
	case err := <-opened:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("OpenOperation = %v", err)
		}
	case <-ctx.Done():
		t.Fatal("stream-credit wait ignored cancellation")
	}
	_ = control.Abort()
	select {
	case <-echoDone:
	case <-ctx.Done():
		t.Fatal("control abort did not release peer")
	}
	if err = client.Close(); err != nil {
		t.Fatal(err)
	}
	if err = client.Err(); err != nil {
		t.Fatalf("normal local close: %v", err)
	}
}
