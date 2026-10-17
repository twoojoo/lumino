# Security policy

Lumino stores database credentials (in the OS keyring), SSH keys and API keys, and talks to
production databases: security reports are taken seriously.

## Reporting a vulnerability

Please **do not open a public issue**. Report it privately through
[GitHub security advisories](https://github.com/twoojoo/lumino/security/advisories/new)
with the affected version, the steps to reproduce and the impact you expect.

You will get an answer within a week. Once a fix is released the advisory is published, crediting
you unless you prefer otherwise.

## Supported versions

Only the latest release receives security fixes; Lumino updates itself to it.

## Scope

Examples of what we want to hear about: credentials leaking to logs, files or the AI provider;
bypasses of read-only or protected connections; SSH host key verification flaws; self-update
installing a release not signed with the Lumino release key.
