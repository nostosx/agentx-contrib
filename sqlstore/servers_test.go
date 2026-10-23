//go:build !integration

package sqlstore_test

// servers is empty in unit builds: the tests run on SQLite only. Build with
// -tags integration to add PostgreSQL and MySQL.
var servers []server
