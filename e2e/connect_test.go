//go:build e2e

package e2e_test

import (
	"errors"
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
