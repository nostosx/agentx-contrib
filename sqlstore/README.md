# SQL state and encrypted MCP credentials

Existing session, loop/workflow checkpoint and memory stores use database/sql
on SQLite, PostgreSQL or MySQL. See the root README for those APIs.

The MCPTokens store (since v1.1.0) also implements mcp.TokenStore:

```go
block, err := aes.NewCipher(key)
if err != nil { return err }
aead, err := cipher.NewGCMWithRandomNonce(block)
if err != nil { return err }
store := sqlstore.MCPTokens(db, "tenant-42/user-7", aead,
    sqlstore.WithDialect(sqlstore.PostgreSQL))
if err := store.Migrate(ctx); err != nil { return err }
cfg := &mcp.OAuthConfig{Store: store}
```

This assembly fragment assumes application-owned db, ctx and a stable key
loaded from its secret manager; imports are crypto/aes, crypto/cipher, this
package and agentx/mcp. The constructor requires a non-empty namespace and
a non-nil, concurrency-safe cipher.AEAD. AES-GCM with caller-generated nonces
and NewGCMWithRandomNonce are supported. Namespaces are at most 255 UTF-8 bytes
without NUL.

Tokens and registrations are encrypted separately and bound to their table,
namespace, kind and full server/issuer key using authenticated additional data.
Keys are SHA-256 digests, so long URLs do not exceed SQL key limits. Missing
records return nil; setting nil deletes idempotently. Returned values do not
alias stored data. Corrupt ciphertext or a wrong key returns an error.

Replicas sharing one identity must use the same namespace and encryption key;
different users must use different namespaces. The application owns access
control and key rotation. Do not generate a new key at every startup: existing
records would become unreadable. Re-encrypt records during planned rotation.

Package Migrate creates mcp_credentials alongside the existing store tables.
Each credential mutation is a single atomic statement. A caller-provided
*sql.Tx can join a larger transaction. Persistence does not coordinate remote
OAuth refresh exchanges; rotating refresh tokens still require coordination
across replicas.

Run MODULES=sqlstore task check lint vulncheck from the repository root.
The integration target requires disposable PostgreSQL and MySQL DSNs and
verifies ten suite/backend combinations, including credentials. Direct tagged
tests can skip an unset server and do not replace that gate.
