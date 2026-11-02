//go:build e2e

package e2e_test

import (
	"context"
	"errors"
	"net"
	"strconv"
	"testing"

	"github.com/kdoqdb/kdoq-go"
)

func TestConnectAnonymously(t *testing.T) {
	srv := startServer(t, settings{})
	client := srv.connect(t, "", "")
	info, level, ok := client.Session()
	if !ok || info.Username != "" || level != kdoq.AccessAdmin || client.State() != kdoq.StateConnected {
		t.Fatalf("session %+v, level %v, %v, state %v", info, level, ok, client.State())
	}
	if healthy, err := client.Server().Health(opContext(t)); err != nil || !healthy {
		t.Fatalf("Health = %v, %v", healthy, err)
	}
	caps, err := client.Server().Capabilities(opContext(t))
	if err != nil || !caps.Capabilities.Has(kdoq.CapDocuments|kdoq.CapQueries) {
		t.Fatalf("Capabilities = %+v, %v", caps, err)
	}
}

func TestConnectWithSCRAMVerifiesTheServer(t *testing.T) {
	srv := startServer(t, settings{Users: []user{{Name: "ann", Password: "correct horse"}}})
	client := srv.connect(t, "ann", "correct horse")
	info, level, ok := client.Session()
	if !ok || info.Username != "ann" || level != kdoq.AccessAdmin {
		t.Fatalf("session %+v, level %v, %v", info, level, ok)
	}
}

func TestWrongPasswordIsRejectedAndAnonymousAccessIsRefused(t *testing.T) {
	srv := startServer(t, settings{Users: []user{{Name: "ann", Password: "correct horse"}}})
	if _, err := srv.dial(opContext(t), "ann", "wrong"); !errors.Is(err, kdoq.ErrAuthentication) {
		t.Fatalf("wrong password = %v, want Authentication", err)
	}
	if _, err := srv.dial(opContext(t), "", ""); !errors.Is(err, kdoq.ErrIncompatible) {
		t.Fatalf("anonymous = %v, want Incompatible: the server offers no NONE", err)
	}
}

func TestAnUnpinnedSelfSignedCertificateIsNotTrusted(t *testing.T) {
	srv := startServer(t, settings{})
	srv.hash = nil
	if _, err := srv.dial(opContext(t), "", ""); err == nil {
		t.Fatal("connected without trusting the certificate")
	}
}

func TestTransportReuseAndNormalClose(t *testing.T) {
	srv := startServer(t, settings{})
	tr, err := srv.transport()
	if err != nil {
		t.Fatal(err)
	}
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(srv.port))
	first, err := tr.Dial(opContext(t), address)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := tr.Dial(opContext(t), address)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if err = first.Close(); err != nil {
		t.Fatal(err)
	}
	closedWithin(t, first.Done())
	if err = first.Err(); err != nil {
		t.Fatalf("normal local close: %v", err)
	}
	if err = first.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	select {
	case <-second.Done():
		t.Fatal("closing one connection affected another")
	default:
	}
	// Dial's context bounds establishment only, not the returned connection.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = tr.Dial(ctx, address); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Dial: %v", err)
	}
}
