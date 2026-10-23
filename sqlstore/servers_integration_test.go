//go:build integration

package sqlstore_test

import "github.com/nostosx/agentx-contrib/sqlstore"

// servers are the database servers the integration tests run on besides
// SQLite. Each variable holds the DSN of a database the tests may create
// and drop tables in; a server whose variable is unset is skipped.
var servers = []server{
	{"postgresql", "AGENTX_IT_POSTGRES_DSN", "pgx", sqlstore.PostgreSQL},
	{"mysql", "AGENTX_IT_MYSQL_DSN", "mysql", sqlstore.MySQL},
}
