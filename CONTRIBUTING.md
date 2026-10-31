# Contributing to A2M

Thanks for helping make A2M more dependable for real coding work.

## Before opening a pull request

1. Open an issue describing the behavior and an observable reproduction, unless it is a small documentation fix.
2. Keep changes focused and add regression tests for confirmed bugs.
3. Run `make test` on a supported development machine. The command builds services before testing them.
4. For desktop changes, run `./scripts/build_desktop.sh` on macOS and visually verify that the WebView is not blank, Connect/Disconnect report real states, and no errors are hidden from the user.
5. Do not include `.env`, credentials, local app bundles, `.dmg` files, generated binary output, logs, private paths or screenshots showing personal account data.
6. Explain how the change affects security permissions and MCP tool schemas. Tool discovery and tool execution are separate states.

## Compatibility

The current desktop build script targets macOS 12+ on Apple Silicon. Rust and Go services are cross-platform in principle, but other packaged desktop targets are not yet validated. Please avoid claiming support for a platform without a repeatable build and a passing end-to-end test.

## Third-party code

Include license attribution and notices for any copied/adapted code or assets. Dependencies and linked upstream projects retain their own licenses. The project references Desktop Commander MCP as an inspiration, not as an automatic statement that all of its implementation is part of A2M.
