# Security policy

Clipferry handles what people copy, which often includes passwords and keys, so security reports are taken seriously.

## Reporting a vulnerability

Please **do not open a public issue**. Report it privately through [GitHub security advisories](https://github.com/bkrdmrcioglu/clipferry/security/advisories/new) instead.

Please include what you found, how to reproduce it and what an attacker could do with it. You will get a reply within a few days, and credit in the release notes if you want it.

## What is in scope

- Pairing (the PAKE exchange over the one-time code) and the encryption between devices
- The local API and MCP endpoint on `127.0.0.1` (authentication, DNS rebinding, cross-origin access)
- The same-network listener and the relay server
- The self-updater (download verification, install step)
- Secret detection that misses something it clearly should catch

## Design notes

The [README's security notes](README.md#security-notes) describe what is protected and what is not, for example that history is stored unencrypted on each device, like any clipboard manager's.
