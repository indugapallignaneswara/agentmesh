# Changelog

All notable changes are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/), and the project aims for
[Semantic Versioning](https://semver.org/) once it reaches 1.0.

## API stability

Pre-1.0, the MCP tool surface may change between minor versions — though in
practice it has only grown. From 1.0, the tool names and their argument shapes
are a stable contract: a breaking change to a tool goes through a deprecation
cycle (the old shape keeps working for at least one minor release, with a
documented replacement). Environment variables and the storage schema follow
the same rule; schema changes are always additive (expand → migrate →
contract, see `docs/operations.md`).

## [Unreleased]

The path from here to 1.0 is tracked in [ROADMAP.md](ROADMAP.md). Milestones
0 through M3 are complete: coordination core, shared task board, review-gated
shared memory, co-edited artifacts, a web dashboard, first-class rooms with
human moderation and invites, at-least-once delivery, rate limiting, and a
security/operability layer (TLS, OAuth 2.1 resource server, Prometheus metrics,
Docker/systemd packaging).

Remaining before 1.0:

- Threshold-gated scale features (channels, vector memory, live push,
  multi-replica) — built only if a real limit is hit.
- The recorded two-machine, cross-vendor acceptance demo.

Since this is the pre-release history, it's summarised rather than itemised;
the git log is authoritative. The first tagged release will begin the itemised
record below.

<!--
## [0.2.0] - YYYY-MM-DD
### Added
### Changed
### Fixed
### Security
-->
