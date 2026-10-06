# Agent instructions

Read `.ai/PROJECT.md` for the project architecture, branch/release conventions and code conventions. The Actions budget policy below supersedes its older CI trigger descriptions.

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

- Main: formatting; build/vet/staticcheck/deadcode/race tests on ready PR or manual dispatch.
- CodeQL runs on ready PR/manual and its existing weekly scan.
- Release no longer repeats analysis on every PR; all release architectures remain.
- Cache cleanup is manual-only; no daily deletion of working build caches.
