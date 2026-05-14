# nolu Changelog

## 0.7.9 — 2026-05-14

### Changed

- Remove vendored `xolu/` subdirectory. nolu has no Go module dependency on
  xolu — the `xolu/` tree was only present to build the `iolu` init container
  binary used by strict-mode demo stacks.

- `Dockerfile.iolu` rewritten to pull `iolu` directly from the published
  `github.com/ha1tch/xolu` module at `v0.9.7-patched98`, eliminating the
  need for a local xolu source checkout.

- `docker-compose.yml`: all four `iolu` init-container build stanzas updated
  from `context: ./xolu` to `context: .` (the nolu repo root), now that
  `Dockerfile.iolu` no longer needs the xolu source tree as build context.

- `Makefile`: removed `vendor` target and its prerequisite from `docker-build`.
  `tidy` no longer re-vendors after tidying. `release` no longer depends on
  `vendor`. `xolu/` exclusions removed from release zip target.

The xolu runtime images in `docker-compose.yml` continue to use
`ghcr.io/ha1tch/xolu:latest`, which now corresponds to `v0.9.7-patched98`.

## 0.7.8 — 2026-05-06

