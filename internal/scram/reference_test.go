package scram_test

// The RFC 7677 section 3 example (SCRAM-SHA-256, user "user", password
// "pencil"), as independent evidence that the exchange computes the proof and
// the server signature the standard defines. The server signature check is
// the one the client relies on before it trusts READY.

import (
	"testing"

	"github.com/kdoqdb/kdoq-go/internal/scram"
)

const (
	nonce       = "rOprNGfwEbeRWgbNEkqO"
	clientFirst = "n,,n=user,r=rOprNGfwEbeRWgbNEkqO"
	serverFirst = "r=rOprNGfwEbeRWgbNEkqO%hvYDpWUa2RaTCAfuxFIlj)hNlF$k0,s=W22ZaJ0SNY7soEsUEjb6gQ==,i=4096"
	clientFinal = "c=biws,r=rOprNGfwEbeRWgbNEkqO%hvYDpWUa2RaTCAfuxFIlj)hNlF$k0,p=dHzbZapWIk4jUhN+Ute9ytag9zjfMHgsqmmiz7AndVQ="
	serverFinal = "v=6rriTRBi23WpRR/wtup+mMhUZUn/dB5nLTJRsjl95G4="
)

func TestClientMatchesTheRFC7677Exchange(t *testing.T) {
	c, err := scram.NewClient("user", "pencil", nonce)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.First(); got != clientFirst {
		t.Fatalf("client-first = %q", got)
	}
	final, err := c.Final(serverFirst)
	if err != nil || final != clientFinal {
		t.Fatalf("client-final = %q, %v\nwant %q", final, err, clientFinal)
	}
	if err := c.Verify(serverFinal); err != nil {
		t.Fatalf("the RFC server signature was rejected: %v", err)
	}
	if err := c.Verify("v=" + "AAAA"); err == nil {
		t.Fatal("a wrong server signature was accepted")
	}
}

func TestClientRefusesAServerNonceThatDoesNotExtendItsOwn(t *testing.T) {
	c, _ := scram.NewClient("user", "pencil", nonce)
	if _, err := c.Final("r=other,s=W22ZaJ0SNY7soEsUEjb6gQ==,i=4096"); err == nil {
		t.Fatal("accepted a foreign nonce")
	}
}
