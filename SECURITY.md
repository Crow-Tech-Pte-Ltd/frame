# Security policy

## Reporting a vulnerability

Please report security issues **privately**. Don't open public issues, pull
requests or discussions for them.

- **GitHub:** [report a vulnerability](https://github.com/Crow-Tech-Pte-Ltd/frame/security/advisories/new)
  ("Security" tab → "Report a vulnerability"), or
- **E-mail:** `hello@crow.sg` (subject "Security")

Please include the affected version (`frame-server version`) or commit, the
component (server, admin panel, installer/deploy files, push relay, iOS or
Android app), reproduction steps and the impact you observed. Test only
against servers you run yourself.

We aim to acknowledge reports within **3 working days** and to agree a
disclosure timeline with you, typically up to 90 days. We'll credit reporters
who want to be credited. There is currently no bug bounty.

## Supported versions

Security fixes go into the latest release. Upgrade with the installer or
`docker compose pull` as described in
[docs/SELF-HOSTING.md](docs/SELF-HOSTING.md#10-upgrading).

## Security model

What Frame protects, against whom, and what it deliberately doesn't do is
described in [docs/SECURITY.md](docs/SECURITY.md). The checklist for
operators is in [docs/SELF-HOSTING.md](docs/SELF-HOSTING.md#14-security-checklist).
