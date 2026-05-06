# CLI Proxy API

## Overview

This fork keeps CLIProxyAPI's core OpenAI/Gemini/Claude/Codex-compatible proxy behavior while focusing on a self-contained management experience for private multi-account deployments.

- Built-in management center: the React management panel is maintained in this repository and embedded into the Go binary as `management.html`, so it no longer depends on external CPAMC releases at runtime.
- Restored usage statistics: request statistics are collected inside CPA and persisted to `usage/usage.json`, with support for local file storage and S3-compatible object storage.
- Improved Codex quota management: Codex credentials are displayed without the old large-list modal limit, grouped by plan (`Pro`, `Plus`, `Free`, `Unknown`), sorted A-Z within each group, and refreshed in the background.
- Codex quota detail: the management center shows plan type, subscription expiry, 5-hour quota windows, and weekly quota windows from CPA-managed quota snapshots.
- Improved auth file view: Codex auth files can be displayed in the same plan-grouped, A-Z sorted layout as quota management, using the same cached quota data.
- Better credential filtering: `usage_limit_reached` is treated as quota exhaustion rather than a broken credential, so real auth problems can be separated from normal quota-limit states.
- Persistent self-hosting path: local Docker deployments can persist config, auth files, logs, and usage data under project-mounted directories; S3-backed deployments persist config, auth files, and usage snapshots in the object store.

## License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.
