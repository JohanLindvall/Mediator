# Security policy

## Reporting a vulnerability

Please report vulnerabilities **privately**, through GitHub's
[private vulnerability reporting](https://github.com/JohanLindvall/Mediator/security/advisories/new)
(the repository's *Security* tab → *Report a vulnerability*). Do not open a
public issue for one.

Say what an attacker can reach and from where, and include the steps or a
request that shows it. The answer comes in the advisory itself, and the fix
lands on `main` and in the next release.

## Supported versions

Only the latest release and the current `main` branch (the
`ghcr.io/johanlindvall/mediator:latest` image) receive fixes.

## What is, and is not, a vulnerability

Mediator is a personal media server meant for a **trusted network**. It has
**no authentication** by design: whoever can reach its port can browse and
stream the library, point it at another directory, and play to a television on
the network. Put it behind a reverse proxy that authenticates if it must be
reachable from anywhere else. That design is not a vulnerability.

What is in scope includes, for example:

- reading or listing a file outside the configured library directories;
- getting past the restrictions a reverse proxy sets per hostname
  (`X-Media-Content`, `X-Allowed-Paths`) — seeing, counting, streaming,
  deleting or changing the preferences of anything a restricted caller should
  not reach;
- forging a signed media URL, or using one for anything but media;
- script injection in the web app, or a cross-origin page changing state;
- a crafted media file, archive, playlist or subtitle that crashes the server,
  exhausts its memory or disk, or runs code.
