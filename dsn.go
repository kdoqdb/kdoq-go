package kdoq

import (
	"net/url"
	"strconv"
	"strings"
)

// DefaultPort is the port a client uses when neither the DSN nor the options
// name one.
const DefaultPort = 27505

// DSN is a parsed connection string.
type DSN struct {
	Host     string
	Port     int
	Username string
	Password string
	Database string
}

// ParseDSN parses kdoq://[user[:pass]@]host[:port][/db]. User, password and
// database may be percent-encoded. The transport URL is https://host:port/native.
// A DSN without a host or with a password but no user parses; Connect rejects it
// unless the options complete it.
func ParseDSN(dsn string) (DSN, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return DSN{}, &ConfigError{Message: "invalid DSN: " + err.Error()}
	}
	if u.Scheme != "kdoq" {
		return DSN{}, &ConfigError{Message: "invalid DSN: expected kdoq://[user[:pass]@]host[:port][/db]"}
	}
	if u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return DSN{}, &ConfigError{Message: "invalid DSN: a query or fragment is not supported"}
	}
	out := DSN{Host: u.Hostname(), Database: strings.TrimPrefix(u.Path, "/")}
	if u.User != nil {
		out.Username = u.User.Username()
		out.Password, _ = u.User.Password()
	}
	if p := u.Port(); p != "" {
		port, err := strconv.Atoi(p)
		if err != nil || port < 1 || port > 65535 {
			return DSN{}, &ConfigError{Message: "invalid DSN port"}
		}
		out.Port = port
	}
	return out, nil
}
