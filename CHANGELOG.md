# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com), and this project adheres to [Semantic Versioning](https://semver.org).

## [Unreleased]

### Fixed

- Point the configuration error's AI-prompt summary line at `gh hush migrate-config` for legacy-field errors instead of falling back to generic "fix the errors" guidance.

## [0.4.0] - 2026-09-11

### Changed

- **Breaking:** Replace the flat `keep`/`hush` booleans with `version: 3`: an explicit `identity`, a terminal `defaults.action`, a `defaults.on_missing_evidence` safety posture, and an ordered list of data-described `rules` evaluated first-match-wins. The matching rule's `name` is the evidence reported in previews.
- Express conditions as a closed, composable vocabulary (`any`, `all`, `not` over `repository`, `subject_type`, `state`, `state_not`, `reason`, `author`, `assignee`, `review_requested`, `review_requested_team`, `mentions_user`, `mentions_team`, `age`), so new protections are rules users write rather than new configuration fields and Go branches. Repository matching supports globs such as `github/dependency-*`, and `state`/`state_not` support `open`, `closed`, `locked`, `merged`, `draft`, and `answered`.
- Derive evidence acquisition from evaluation: each predicate fetches only the GitHub resource it inspects, at most once per notification, which removes the hand-maintained mirror between evidence planning and classification.
- Keep safety non-configurable and evaluated before any rule: only `Issue`, `PullRequest`, `Discussion`, `Commit`, `Release`, and `CheckSuite` are eligible for hushing.
- Write the version 3 recommended policy from `gh hush init-config`, reproducing the previous default protections as explicit, editable rules.

### Added

- Add `--quiet` to suppress the preview and print only a concise result to stderr.
- Add `gh hush migrate-config`, which mechanically rewrites a pre-v3 configuration as an equivalent version 3 policy. It previews by default and requires `--write` to rewrite the file in place, keeping a `.bak` backup.

### Fixed

- Reject recursive and overly deep rule conditions during decoding instead of crashing with a stack overflow.
- Evaluate `all` with defensible indeterminate semantics: a conclusive non-match wins over an evidence-limited sibling regardless of predicate order.
- Scope `mentions_team` to the notification repository's owner, matching the `review_requested_team` behavior.
- Require rule condition values to be non-empty strings, restrict `state`/`state_not` to `open`, `closed`, `locked`, `merged`, `draft`, and `answered`, and require `search` to accompany exactly one mention predicate, keeping the JSON Schema and runtime parser in agreement.
- Force owner-only (`0600`) permissions on the migrated configuration and its `.bak` backup, and reject unknown fields or multiple documents in the source configuration during migration.

## [0.3.0] - 2026-09-09

### Added

- Add `gh hush init-config` to create a valid conservative starter policy from explicit user, organization, and optional team values without contacting GitHub or overwriting an existing file.
- Add the optional `watched_repositories` configuration map, keyed by `owner/repo`, with the additive `all_notifications`, `open_pull_requests`, `open_issues`, and `open_discussions` capabilities. Existing configurations remain valid.

### Fixed

- Replace generic help on a missing default configuration with an actionable error that includes the expected path and exact initialization command, including when run flags are present.

## [0.2.2] - 2026-09-05

### Added

- Include concise agent-ready prompts with configuration validation errors, including an exact migration prompt for renamed team slugs.

## [0.2.1] - 2026-09-05

### Fixed

- Report all YAML configuration problems together with actionable guidance for unknown, removed, and renamed fields, including the required v0.2.0 migration.

## [0.2.0] - 2026-09-05

### Added

- Keep open pull requests with a current review request for a configured team while allowing closed or merged pull requests to proceed through normal hush policy.
- Publish a documented JSON Schema for configuration and add `gh hush validate-config` for validation without contacting GitHub.

### Changed

- **Configuration migration required:** rename `discussion_team_slugs` to `team_slugs` and add the required `keep.active_team_review_requested_pull_requests` boolean.

## [0.1.5] - 2026-08-27

### Fixed

- Guarantee preview URL fallbacks remain browser-facing even when GitHub omits or returns an unsafe repository `html_url`.
- Build the current checkout before installing it as a local extension, preventing stale binaries from surviving a relink.

## [0.1.4] - 2026-08-24

### Fixed

- Show subject browser URLs from GitHub's `html_url` in preview reports instead of exposing API subject URLs, with validated repository links as a safe fallback.
- Keep display-only URL enrichment isolated from policy classification and avoid repeating display-only requests during apply revalidation.

## [0.1.3] - 2026-08-24

### Fixed

- Report classification elapsed timing when the unread inbox is empty.

## [0.1.2] - 2026-08-24

### Changed

- Replace the dense one-line apply summary with an aligned, multi-line "Application summary" block that groups targets, unsubscribe, mark Done, skipped, and elapsed metrics for easier reading.

## [0.1.1] - 2026-08-24

### Security

- Isolate GitHub API traffic behind an origin-locked transport so requests cannot be redirected to a different host.
- Harden GitHub transport error redaction to avoid leaking sensitive request data in error messages.

## [0.1.0] - 2026-08-23

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

- Report the installed release version with `--version`.
- Report concise elapsed timings on stderr for the major phases (authentication and inbox listing, classification, preview report generation, apply) plus total runtime, using an injectable clock for deterministic tests. The apply summary carries an aggregate `elapsed`, and total runtime excludes the interactive confirmation wait.
- Default preview-first workflow with an interactive, default-No confirmation prompt.
- `--confirm` for explicitly applying proposed notification updates without a prompt.
- `--dry-run` for a guaranteed preview-only run.
- Initial release.

[Unreleased]: https://github.com/maxbeizer/gh-hush/compare/v0.4.0...HEAD
[0.4.0]: https://github.com/maxbeizer/gh-hush/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/maxbeizer/gh-hush/compare/v0.2.2...v0.3.0
[0.2.2]: https://github.com/maxbeizer/gh-hush/compare/v0.2.1...v0.2.2
[0.2.1]: https://github.com/maxbeizer/gh-hush/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/maxbeizer/gh-hush/compare/v0.1.5...v0.2.0
[0.1.5]: https://github.com/maxbeizer/gh-hush/compare/v0.1.4...v0.1.5
[0.1.4]: https://github.com/maxbeizer/gh-hush/compare/v0.1.3...v0.1.4
[0.1.3]: https://github.com/maxbeizer/gh-hush/compare/v0.1.2...v0.1.3
[0.1.2]: https://github.com/maxbeizer/gh-hush/compare/v0.1.1...v0.1.2
[0.1.1]: https://github.com/maxbeizer/gh-hush/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/maxbeizer/gh-hush/releases/tag/v0.1.0
