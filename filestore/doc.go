// Package filestore keeps the state of agentx in plain files: sessions,
// run checkpoints, workflow checkpoints, long-term memory, RAG chunks and
// MCP OAuth tokens. Each constructor returns a store for one core
// contract:
//
//	Sessions(dir)            session.Store
//	Checkpoints(dir)         loop.Checkpointer
//	WorkflowCheckpoints(dir) workflow.Checkpointer
//	Memory(path)             memory.Store and memory.ScopedDeleter
//	Vectors(dir)             rag.VectorStore and rag.Revisioner
//	MCPTokens(path)          mcp.TokenStore
//
// Files are JSON, replaced through a temporary file and os.Rename. Replacement
// is atomic on Unix; other platforms follow os.Rename's guarantees. New files
// use mode 0600 and new directories mode 0700, subject to platform permissions;
// existing directories retain their permissions. Storage paths must be trusted.
// Sessions and checkpoints lock each JSON file through a persistent .lock
// sidecar during reads, replacements and deletes. Keep these sidecars in place;
// removing one while a store is active can allow conflicting locks.
//
// Concurrency guarantees differ by store: MemoryStore coordinates processes,
// SessionStore serializes saves within one instance, TokenStore serializes
// updates within one instance, and VectorStore keeps an instance-local snapshot.
// See each type before sharing a path between writers. For database-backed
// storage, use github.com/nostosx/agentx-contrib/sqlstore.
//
//	sessions, err := filestore.Sessions(".agentx/sessions")
//	if err != nil { ... }
//	runner := agent.NewRunner(a, sessions)
package filestore
