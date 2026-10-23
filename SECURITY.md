# Security policy

Starting with v1.0.0, security fixes are provided for the latest minor release
of each module's v1 series. Modules are versioned independently. Upgrade to
the latest patch release before reporting a vulnerability. Reports about the
current development code are also welcome.

Use GitHub's **Security → Report a vulnerability** form when private reporting
is enabled. If it is unavailable, open an issue asking for a private contact
without disclosing exploit details. Include the affected module, revision,
prerequisites, impact and a minimal synthetic reproduction. Do not include
credentials, personal data or access-bearing URLs.

For defects in the core, follow the
[agentx security policy](https://github.com/nostosx/agentx/blob/main/SECURITY.md).

Notes on the modules of this repository:

- **provider** sends API keys and tokens only to the configured base URL.
  Redirects must keep its scheme, hostname and effective port, including
  when a custom HTTP client's redirect policy permits other destinations.
  Errors and telemetry do not include credentials; report it if one does.
- **filestore** writes files with mode 0600 in directories created with
  mode 0700. Anyone who can read them can read the sessions, memories and,
  for `MCPTokens`, the OAuth tokens they hold.
- **sqlstore** passes every value as a query parameter; table names come
  from `WithPrefix`, which accepts only letters, digits and underscores.
- **otel** records message content only with `RecordContent`; it may
  include personal data.

Each module lists its dependencies in its `go.mod`; CI checks every module
on each pull request.
