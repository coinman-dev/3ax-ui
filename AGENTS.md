# Agent instructions

Read `.ai/PROJECT.md` for what 3AX-UI is and how it relates to upstream, then upstream's own guides: `CLAUDE.md`, `frontend/CLAUDE.md` and, on demand, `docs/architecture.md`. They apply in full unless this file or `.ai/PROJECT.md` says otherwise. The Actions budget policy below supersedes their CI descriptions.

## GitHub Actions budget policy

The owner requested minimal necessary cloud work on 2026-10-06 after an Actions-minute alert. This applies to public and private repositories; public standard runners may be free, but redundant work is still unwanted.

- Develop and run focused checks locally on `dev`. Do not restore expensive automatic builds on every `dev` push.
- `main` runs cheap checks where useful. Full validation runs on a ready (non-draft) PR, explicit manual dispatch, or as a release gate.
- Before a direct behavior change in `main` that bypasses PR validation, run the appropriate full validation manually; documentation and policy-only edits do not need a full build. Cheap checks do not prove the application passes its full tests.
- Build/install/package Windows, macOS, Android release variants and architecture matrices only for `v*` prerelease/release tags or an explicit manual request. A tag containing `-` is a prerelease; a stable tag has no suffix.
- Preserve security, race/regression, signing, license and installer checks required for release quality. Do not make required checks nonblocking to save minutes.
- Cancel obsolete validation runs by workflow/ref. Serialize publication for the same tag without cancelling an in-progress release. Set bounded job timeouts.
- Reuse Rust/Go/Gradle/npm/Docker caches. Do not add daily cache deletion; it wastes compilation time. Artifact expiry and cache eviction are different from consumed execution minutes.
- Keep short artifact retention (normally 3 days; release assets remain attached to Releases). Keep only schedules with a concrete purpose, such as fresh vulnerability data or live-provider contracts.
- Do not trigger full builds, push test tags, publish releases or rerun the entire matrix merely to check workflow edits. Use actionlint, YAML validation and event/condition review first.
- Maintain this policy when adding workflows. If a required check is introduced in branch protection, account for draft PRs and path filters so it cannot remain pending forever.

## This repository

- Fork of [MHSanaei/3x-ui](https://github.com/MHSanaei/3x-ui), rebuilt on upstream `main` on 2026-11-19 (remote `upstream`). Merge `upstream/main` into `dev` regularly; keep our code in our own files so merges stay cheap.
- CI (`ci.yml`): a push to `main` runs only `golangci` and `shell-lint`. A ready PR, a manual dispatch or the release gate (`workflow_call`) runs the full set: race tests (they replace upstream's separate plain `go test` job), PostgreSQL, node e2e, fuzz smoke, codegen, govulncheck, frontend with Storybook and audit.
- CodeQL runs on ready PR, manual dispatch and its weekly scan.
- Release (`release.yml`): `v*` tags or manual dispatch only; the full `ci.yml` runs first as the gate; all Linux architectures plus Windows; a tag with `-` is a prerelease; the tag must equal `v` + `internal/config/version`. No rolling `dev-latest` build.
- Docker: tags only, image `ghcr.io/coinman-dev/3x-ui` (the pre-rebrand name, kept for existing compose files).
- Removed from upstream's CI: Claude issue/PR workflows, mutation testing, the docs site build and deploy. `docs/` stays untouched except `docs/public/openapi.json`, which `make verify` requires to equal `frontend/public/openapi.json`.
- Cache cleanup is manual-only; no daily deletion of working build caches. Dependabot tracks GitHub Actions only, monthly; Go and npm updates arrive with upstream merges.
