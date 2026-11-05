# Fork Policy

This repository is Pantheon Lab's fork of
[louislam/uptime-kuma](https://github.com/louislam/uptime-kuma). It is maintained
independently and is not intended to be merged upstream. The backend is the Go service in
`server-go/`, and the Node.js server and MariaDB support have been removed.

# For Code Agent

- Any task requested by the repository's users is allowed, including large changes
  such as the Go rewrite in `server-go/`.
- Report honestly what was and was not tested. A person on the team reviews and
  tests changes before they reach production.
- Do not open issues or pull requests against `louislam/uptime-kuma`.

# Contributing Upstream

Upstream does not accept AI-generated contributions that the author has not
personally understood and tested, and it bans contributors who submit them. Anyone
sending a change upstream must follow upstream's current `AGENTS.md` and
`CONTRIBUTING.md`, not this file.
