// Package cubrid provides a pure-Go database/sql driver for CUBRID databases.
//
// DSN format:
//
//	cubrid://user:password@host:port/database[?autocommit=true]
//
// Example:
//
//	db, err := sql.Open("cubrid", "cubrid://dba:@localhost:33000/demodb")
//
// GORM example:
//
//	import "github.com/cubrid-labs/cubrid-go/dialector"
//
//	db, err := gorm.Open(cubrid.Open("cubrid://dba:@localhost:33000/demodb"), &gorm.Config{})
package cubrid

import (
	"database/sql"
	"database/sql/driver"
	"fmt"
	"net/url"
	"strconv"
	"time"
)

func init() {
	sql.Register("cubrid", &Driver{})
}

// Driver implements database/sql/driver.Driver.
type Driver struct{}

// Open parses the DSN and returns a new connection.
func (d *Driver) Open(dsn string) (driver.Conn, error) {
	cfg, err := parseDSN(dsn)
	if err != nil {
		return nil, err
	}
	c := &conn{
		host:       cfg.host,
		port:       cfg.port,
		database:   cfg.database,
		user:       cfg.user,
		password:   cfg.password,
		timeout:    cfg.timeout,
		autoCommit: cfg.autoCommit,
	}
	if err := c.connect(); err != nil {
		return nil, err
	}
	return c, nil
}

// config holds parsed DSN parameters.
type config struct {
	host       string
	port       int
	database   string
	user       string
	password   string
	timeout    time.Duration
	autoCommit bool
}

// parseDSN parses a CUBRID DSN URL:
// cubrid://[user[:password]]@host[:port]/database[?autocommit=true&timeout=30s]
func parseDSN(dsn string) (*config, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return nil, fmt.Errorf("cubrid: invalid DSN %q: %w", dsn, err)
	}

	if u.Scheme != "cubrid" {
		return nil, fmt.Errorf("cubrid: DSN scheme must be 'cubrid', got %q", u.Scheme)
	}

	cfg := &config{
		host:       "localhost",
		port:       33000,
		autoCommit: true,
		timeout:    30 * time.Second,
	}

	if u.Hostname() != "" {
		cfg.host = u.Hostname()
	}
	if portStr := u.Port(); portStr != "" {
		p, err := strconv.Atoi(portStr)
		if err != nil {
			return nil, fmt.Errorf("cubrid: invalid port %q: %w", portStr, err)
		}
		cfg.port = p
	}

	// /database → strip leading slash
	if len(u.Path) > 1 {
		cfg.database = u.Path[1:]
	}

	if u.User != nil {
		cfg.user = u.User.Username()
		cfg.password, _ = u.User.Password()
	}

	q := u.Query()
	if v := q.Get("autocommit"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.autoCommit = b
		}
	}
	if v := q.Get("timeout"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.timeout = d
		}
	}

	if cfg.database == "" {
		return nil, fmt.Errorf("cubrid: database name is required in DSN")
	}

	return cfg, nil
}
