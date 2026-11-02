// Package scram implements SCRAM-SHA-256 without channel binding ("n,,") as
// the native AUTH exchange uses it: the client side, and the server side that
// the in-process fake server needs.
package scram

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ErrAuth is wrapped by every failure of the exchange.
var ErrAuth = errors.New("authentication failed")

func fail(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrAuth, fmt.Sprintf(format, args...))
}

const maxIterations = 1_000_000

func attributes(message string) map[string]string {
	out := map[string]string{}
	for _, part := range strings.Split(message, ",") {
		if i := strings.IndexByte(part, '='); i > 0 {
			out[part[:i]] = part[i+1:]
		}
	}
	return out
}

func hmacSHA256(key, data []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(data)
	return m.Sum(nil)
}

func escapeName(name string) string {
	return strings.NewReplacer("=", "=3D", ",", "=2C").Replace(name)
}

// Client is one client-side SCRAM exchange.
type Client struct {
	password string
	nonce    string
	bare     string
	expected string
}

// NewClient starts an exchange. A nonce is generated when nonce is empty.
func NewClient(username, password, nonce string) (*Client, error) {
	if nonce == "" {
		raw := make([]byte, 18)
		if _, err := rand.Read(raw); err != nil {
			return nil, err
		}
		nonce = base64.StdEncoding.EncodeToString(raw)
	}
	return &Client{
		password: password,
		nonce:    nonce,
		bare:     "n=" + escapeName(username) + ",r=" + nonce,
	}, nil
}

// First returns the client-first message.
func (c *Client) First() string { return "n,," + c.bare }

// Final answers the server-first message with the client-final message.
func (c *Client) Final(serverFirst string) (string, error) {
	a := attributes(serverFirst)
	nonce, salt := a["r"], a["s"]
	iterations, err := strconv.Atoi(a["i"])
	if !strings.HasPrefix(nonce, c.nonce) || salt == "" || err != nil || iterations < 1 || iterations > maxIterations {
		return "", fail("invalid server-first message")
	}
	saltBytes, err := base64.StdEncoding.DecodeString(salt)
	if err != nil {
		return "", fail("invalid salt")
	}
	salted, err := pbkdf2.Key(sha256.New, c.password, saltBytes, iterations, sha256.Size)
	if err != nil {
		return "", fail("key derivation: %v", err)
	}
	withoutProof := "c=biws,r=" + nonce
	authMessage := c.bare + "," + serverFirst + "," + withoutProof
	clientKey := hmacSHA256(salted, []byte("Client Key"))
	storedKey := sha256.Sum256(clientKey)
	signature := hmacSHA256(storedKey[:], []byte(authMessage))
	proof := make([]byte, len(clientKey))
	for i := range proof {
		proof[i] = clientKey[i] ^ signature[i]
	}
	serverKey := hmacSHA256(salted, []byte("Server Key"))
	c.expected = base64.StdEncoding.EncodeToString(hmacSHA256(serverKey, []byte(authMessage)))
	return withoutProof + ",p=" + base64.StdEncoding.EncodeToString(proof), nil
}

// Verify checks the server-final signature. READY must not be trusted before
// it succeeds.
func (c *Client) Verify(serverFinal string) error {
	got := attributes(serverFinal)["v"]
	if c.expected == "" || subtle.ConstantTimeCompare([]byte(got), []byte(c.expected)) != 1 {
		return fail("server signature mismatch")
	}
	return nil
}

// Server is one server-side exchange.
type Server struct {
	salted     []byte
	salt       []byte
	iterations int
	nonce      string
	bare       string
	first      string
}

// NewServer prepares an exchange for a user with the given password. salt and
// suffix are fixed by tests; empty values are generated.
func NewServer(password string, salt []byte, iterations int, suffix string) (*Server, error) {
	if len(salt) == 0 {
		salt = make([]byte, 16)
		if _, err := rand.Read(salt); err != nil {
			return nil, err
		}
	}
	if suffix == "" {
		raw := make([]byte, 18)
		if _, err := rand.Read(raw); err != nil {
			return nil, err
		}
		suffix = base64.StdEncoding.EncodeToString(raw)
	}
	salted, err := pbkdf2.Key(sha256.New, password, salt, iterations, sha256.Size)
	if err != nil {
		return nil, err
	}
	return &Server{salted: salted, salt: salt, iterations: iterations, nonce: suffix}, nil
}

// Username extracts the user name of a client-first message.
func Username(clientFirst string) (string, error) {
	bare, ok := strings.CutPrefix(clientFirst, "n,,")
	if !ok {
		return "", fail("unsupported GS2 header")
	}
	name := attributes(bare)["n"]
	return strings.NewReplacer("=3D", "=", "=2C", ",").Replace(name), nil
}

// First answers the client-first message with the server-first message.
func (s *Server) First(clientFirst string) (string, error) {
	bare, ok := strings.CutPrefix(clientFirst, "n,,")
	if !ok {
		return "", fail("unsupported GS2 header")
	}
	s.bare = bare
	clientNonce := attributes(bare)["r"]
	if clientNonce == "" {
		return "", fail("missing client nonce")
	}
	s.nonce = clientNonce + s.nonce
	s.first = fmt.Sprintf("r=%s,s=%s,i=%d", s.nonce, base64.StdEncoding.EncodeToString(s.salt), s.iterations)
	return s.first, nil
}

// Final verifies the client proof and returns the server-final message.
func (s *Server) Final(clientFinal string) (string, error) {
	a := attributes(clientFinal)
	proofText, ok := a["p"]
	if !ok || a["r"] != s.nonce {
		return "", fail("invalid client-final message")
	}
	proof, err := base64.StdEncoding.DecodeString(proofText)
	if err != nil {
		return "", fail("invalid proof")
	}
	withoutProof := clientFinal[:strings.LastIndex(clientFinal, ",p=")]
	authMessage := s.bare + "," + s.first + "," + withoutProof
	clientKey := hmacSHA256(s.salted, []byte("Client Key"))
	storedKey := sha256.Sum256(clientKey)
	signature := hmacSHA256(storedKey[:], []byte(authMessage))
	if len(proof) != len(signature) {
		return "", fail("bad proof")
	}
	recovered := make([]byte, len(proof))
	for i := range proof {
		recovered[i] = proof[i] ^ signature[i]
	}
	check := sha256.Sum256(recovered)
	if subtle.ConstantTimeCompare(check[:], storedKey[:]) != 1 {
		return "", fail("bad proof")
	}
	serverKey := hmacSHA256(s.salted, []byte("Server Key"))
	return "v=" + base64.StdEncoding.EncodeToString(hmacSHA256(serverKey, []byte(authMessage))), nil
}

// ForgedFinal returns a server-final message with a wrong signature, for
// tests of the client's verification.
func ForgedFinal() string {
	return "v=" + base64.StdEncoding.EncodeToString(make([]byte, sha256.Size))
}
