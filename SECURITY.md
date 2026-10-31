# Security policy

## Reporting a vulnerability

Please **do not disclose a vulnerability publicly** in GitHub Issues before the maintainers can assess it. Use GitHub's private vulnerability reporting feature for this repository, if enabled, or contact the maintainer through the private contact channel linked from the GitHub profile.

Do not include live API keys, tunnel IDs, access tokens, usernames, home-directory paths, customer data or private repository source in a report. If a minimal reproducible case is needed, use fictional values and a disposable test project.

## Security model

A2M exposes local filesystem, terminal and document functionality through MCP. Those capabilities can be powerful and potentially destructive. The AI client is the decision-maker; A2M's responsibility is to enforce local authentication, scoping and human-visible connection controls.

- Prefer local loopback listeners and an outbound Secure MCP Tunnel for remote OpenAI clients.
- Use only the dedicated OpenAI runtime credential required for your own tunnel, stored locally and scoped narrowly.
- Keep `.env`, Keychain exports, tunnel-client profiles and logs containing tokens out of Git.
- Capability discovery must not silently execute scripts or expose third-party MCP credentials.
- Treat externally installed skills and MCP servers as additional trust boundaries and review any explicitly granted permissions.
- Disconnect the bridge when you do not intend to allow AI-initiated machine operations.

## Release policy

A release is not security-verified merely because its UI launches. It must pass unit/integration tests, a clean installation, a source/binary secret scan and a real MCP call from an intended client. Public installer signatures and notarization status must be stated truthfully.
