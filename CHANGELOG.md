# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com), and this project adheres to [Semantic Versioning](https://semver.org).

## [Unreleased]

### Changed

- Replace the public `unsubscribe` action with `unsubscribe_and_mark_done`.
- Fetch all active inbox notifications, including read notifications not marked Done.
- Revalidate each approved target with a fresh inbox snapshot and fresh policy evidence before sequential mutation.
- Unsubscribe with `DELETE /notifications/threads/{id}/subscription`, mark Done with `DELETE /notifications/threads/{id}`, and verify removal from the active inbox.
- Retry transient network, rate-limit, and service errors at most three attempts, honoring GitHub retry headers.
- Report revalidation-evidence, unsubscribe, Done, and verification outcomes separately and return nonzero for unresolved partial failures while continuing later targets.
- Protect external-organization notifications for every subject type.
- Protect Discussions when an exact configured team mention appears anywhere in the complete paginated comment history.
- Use an explicit catch-all allowlist: Issue, PullRequest, Discussion, Commit, Release, and CheckSuite; safety-keep every other subject type.
- Make a hard configuration schema break: replace `unsubscribe` with `hush`, remove `run_mode`, and remove `output`.

### Fixed

- Mark notifications Done instead of merely marking them read. PATCHing a notification thread marks it read but does not remove it from GitHub's inbox.
- Treat preview evidence failures as conservative safety keeps rather than command failures, so a run with no eligible targets returns zero as documented; fresh revalidation evidence failures still return nonzero.
- Resolve relative pagination links against the final URL after same-origin redirects.
- Pin token lookup to `github.com` so `GH_HOST` cannot cause an enterprise token to be sent to `api.github.com`.

### Added

- Default preview-first workflow with an interactive, default-No confirmation prompt.
- `--confirm` for explicitly applying proposed notification updates without a prompt.
- `--dry-run` for a guaranteed preview-only run.
- Initial release.
