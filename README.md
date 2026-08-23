# gh-hush

`gh-hush` is a safe, explainable GitHub notification triage extension. It fetches every active inbox notification—including read notifications that have not been marked Done—through the account authenticated by `gh`, evaluates a user-owned policy, and previews every decision before making changes.

## Install

```bash
gh extension install maxbeizer/gh-hush
```

For a local checkout:

```bash
make build
make install-local
```

## Usage

```bash
gh hush             # preview; prompt with default No when fully interactive
gh hush --dry-run   # preview only
gh hush --confirm   # preview and apply without prompting
```

A no-flag invocation is preview-only unless stdin, preview output, and prompt output are all interactive terminals. Redirected or piped execution requires `--confirm` to mutate GitHub. `--dry-run` and `--confirm` are mutually exclusive.

The complete preview unconditionally shows every notification's URL, subject type, repository, reason, proposed action, and matching policy evidence. Authentication, active-inbox listing, configuration, and report-generation failures return nonzero without mutation. A required preview evidence failure is reported and conservatively safety-keeps that notification; it is not an eligible mutation target. Declining confirmation, having no eligible targets, a target's disappearance, and a genuine newly matching keep rule return zero.

For each approved `unsubscribe_and_mark_done` target, gh-hush sequentially:

1. refetches the active inbox and reevaluates the target using fresh policy evidence;
2. skips it successfully if it disappeared or now genuinely matches a keep/safety rule, but records a failure if required fresh evidence is unavailable;
3. unsubscribes with `DELETE /notifications/threads/{id}/subscription`;
4. marks it Done with `DELETE /notifications/threads/{id}`; and
5. refetches the active inbox to verify that it disappeared.

It never marks a target Done when unsubscribe fails. Item failures do not prevent later targets from being attempted. The final application summary separately reports revalidation, unsubscribe, Done, and verification outcomes; unavailable revalidation evidence and every unresolved mutation or verification failure return nonzero.

Temporary network errors and HTTP 429, 502, 503, and 504 responses are attempted at most three times. Retries honor GitHub retry/rate-limit headers and otherwise use exponential backoff with jitter. Cancellation stops retries immediately.

Marking Done removes the current notification from the inbox; it is not the same as PATCHing a thread to mark it read. Hushing is not a permanent ignore: a future personal mention, assignment, or individual review request can bring the thread back.

## Configuration

The default path is `$XDG_CONFIG_HOME/gh-hush/config.yml`, or `~/.config/gh-hush/config.yml` when `XDG_CONFIG_HOME` is unset. Override it with `--config PATH`.

```yaml
user: YOUR-GITHUB-LOGIN
github_organization: YOUR-PRIMARY-ORGANIZATION

discussion_team_slugs:
  - YOUR-PRIMARY-ORGANIZATION/YOUR-TEAM

keep:
  external_organization_issues: true
  personally_mentioned: true
  personally_assigned: true
  individually_review_requested: true
  authored_by_user: true
  team_mentioned_discussions: true

hush:
  all_other_notifications: true
```

This schema is intentionally incompatible with earlier versions: `run_mode`, `unsubscribe`, and the entire `output` section are unknown fields and are rejected. Every keep boolean is required (and may be `false`); `hush.all_other_notifications` is required and must be `true`. Complete previews are unconditional. The configured user must match the authenticated account.

Keep rules protect:

1. notifications from repositories outside `github_organization`, for every subject type;
2. `reason: mention`;
3. `reason: assign` or a current personal assignment;
4. a current individual pull-request review request (team-only requests do not match);
5. work authored by `user`; and
6. Discussions containing an exact configured team mention in the body or anywhere in the complete paginated comment history.

Required evidence failures conservatively safety-keep a notification. Discussion team mentions found in historical comments continue to protect the Discussion until it is manually resolved.

Only `Issue`, `PullRequest`, `Discussion`, `Commit`, `Release`, and `CheckSuite` are eligible for the catch-all hush action. Unsupported, unknown, sensitive, administrative, and security-related subject types are safety-kept.

> The historical configuration key `external_organization_issues` is retained, but its protection now intentionally applies to all notification subject types.

## Development

```bash
make build
make test
make ci
make lint
```
