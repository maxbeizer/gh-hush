# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com), and this project adheres to [Semantic Versioning](https://semver.org).

## [Unreleased]

### Changed

- Replace the public `unsubscribe` action with `unsubscribe_and_mark_done`.
- Fetch unread notifications only because GitHub's `all=true` listing cannot distinguish read inbox entries from Done/history records.
- Revalidate each approved target with a fresh thread record and fresh policy evidence before sequential mutation, conservatively skipping records that are no longer unread.
- Unsubscribe with `DELETE /notifications/threads/{id}/subscription` and mark Done with `DELETE /notifications/threads/{id}`; treat GitHub's successful Done response as success because thread GET continues returning historical records.
- Retry transient network, rate-limit, and service errors at most three attempts, honoring GitHub retry headers.
- Report revalidation-evidence, unsubscribe, and Done outcomes separately and return nonzero for unresolved partial failures while continuing later targets.
- Protect external-organization notifications for every subject type.
- Protect Discussions when an exact configured team mention appears anywhere in the complete paginated comment history.
- Use an explicit catch-all allowlist: Issue, PullRequest, Discussion, Commit, Release, and CheckSuite; safety-keep every other subject type.
- Make a hard configuration schema break: replace `unsubscribe` with `hush`, remove `run_mode`, and remove `output`.

### Fixed

- Remove the invalid post-Done disappearance check: the individual thread endpoint returns historical records after a successful Done request.
- Prevent `all=true` discovery from reprocessing Done/history records; the public REST API does not expose a reliable distinction between those records and read-but-active entries.
- Mark notifications Done instead of merely marking them read. PATCHing a notification thread marks it read but does not remove it from GitHub's inbox.
- Treat preview evidence failures as conservative safety keeps rather than command failures, so a run with no eligible targets returns zero as documented; fresh revalidation evidence failures still return nonzero.
- Resolve relative pagination links against the final URL after same-origin redirects.
- Pin token lookup to `github.com` so `GH_HOST` cannot cause an enterprise token to be sent to `api.github.com`.

### Added

- Default preview-first workflow with an interactive, default-No confirmation prompt.
- `--confirm` for explicitly applying proposed notification updates without a prompt.
- `--dry-run` for a guaranteed preview-only run.
- Initial release.
