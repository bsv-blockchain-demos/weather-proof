package postgres

import (
	"net"
	"net/url"
	"strconv"

	"github.com/bsv-blockchain-demos/weather-proof/internal/config"
)

// DSNParts is a Postgres connection string assembled from separate pieces.
//
// It exists so that no call site ever builds a connection string by
// concatenating a password into a URL. url.UserPassword percent-encodes the
// credential, and net.JoinHostPort handles a bracketed IPv6 host, which naive
// "host:port" formatting does not (nosprintfhostport flags exactly that).
//
// Password is a config.Secret and this package deliberately declares neither a
// Secret of its own nor an alias to one: one definition, in a leaf package,
// shared with SERVER_PRIVATE_KEY and TEMPEST_API_KEY, is what stops a second
// copy drifting away from the first and losing MarshalJSON.
//
// Note what is deliberately absent: pool_max_conns, pool_min_conns and
// pool_min_idle_conns. pgxpool.ParseConfig honors those as DSN runtime
// params, so leaving them out of the DSN is what stops a connection string
// silently overriding the values PoolConfig sets in Go. default_query_exec_mode
// is likewise never emitted here, and PoolConfig rejects it if it arrives from
// anywhere else.
type DSNParts struct {
	Host     string
	Port     int
	User     string
	Password config.Secret
	Database string
	SSLMode  string
}

// DSN renders the parts as a postgres:// URL.
func (p DSNParts) DSN() string {
	u := &url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(p.User, p.Password.Reveal()),
		Host:   net.JoinHostPort(p.Host, strconv.Itoa(p.Port)),
		Path:   "/" + p.Database,
	}
	q := url.Values{}
	q.Set("sslmode", p.SSLMode)
	u.RawQuery = q.Encode()
	return u.String()
}
