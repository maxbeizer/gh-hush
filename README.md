# gh-hush

`gh-hush` is a safe, explainable GitHub notification triage extension. It fetches unread notifications through the account authenticated by `gh`, evaluates a user-owned policy, and previews every decision before making changes.

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
gh hush --debug     # add request/workflow diagnostics on stderr
```

A no-flag invocation is preview-only unless stdin, preview output, and prompt output are all interactive terminals. Redirected or piped execution requires `--confirm` to mutate GitHub. `--dry-run` and `--confirm` are mutually exclusive.

The complete preview unconditionally shows every discovered notification's URL, subject type, repository, reason, proposed action, and matching policy evidence. Authentication, notification listing, configuration, and report-generation failures return nonzero without mutation. A required preview evidence failure is reported and conservatively safety-keeps that notification; it is not an eligible mutation target. Declining confirmation, having no eligible targets, a missing target record, a target that is no longer unread, and a genuine newly matching keep rule return zero.

For each approved `unsubscribe_and_mark_done` target, gh-hush uses a pool of at most four workers. Operations for each individual thread remain strictly sequential:

1. fetch the thread record again;
2. skip it if it is missing or no longer unread, otherwise reevaluate it using fresh policy evidence;
3. skip it if it now matches a keep/safety rule, or record a failure if required fresh evidence is unavailable;
4. unsubscribe with `DELETE /notifications/threads/{id}/subscription`; and
5. mark it Done with `DELETE /notifications/threads/{id}`.

It never marks a target Done when unsubscribe fails. A successful 2xx response to the Done request (documented by GitHub as `204`) is treated as success; gh-hush does not perform an unsupported disappearance check afterward. Item failures do not prevent later targets from being attempted. The final application summary separately reports revalidation, unsubscribe, and Done outcomes; unavailable revalidation evidence and mutation failures return nonzero.

### GitHub notification API limitation

GitHub's individual thread endpoint can return a historical record after the thread has been marked Done. The `GET /notifications?all=true` listing also includes Done/history records, while the returned REST representation provides no reliable field that distinguishes those records from read notifications still in the inbox. Consequently, gh-hush cannot safely process read-but-still-inbox notifications without risking repeat mutations of Done history.

As a conservative tradeoff, discovery uses GitHub's default unread-only notification listing, and pre-mutation revalidation requires the fresh thread record to remain `unread: true`. A retrievable thread record proves only that the record exists, not that it is in the active inbox. Read notifications must be handled manually (or made unread before a later run). This avoids claiming unsupported active-inbox membership guarantees, though it cannot eliminate changes that race with a request already in progress.

Temporary network errors and HTTP 429, 502, 503, and 504 responses are attempted at most three times. Retries honor GitHub retry/rate-limit headers and otherwise use exponential backoff with jitter. Cancellation stops retries immediately.

### Debug diagnostics

`--debug` adds structured, line-oriented diagnostics to stderr for both preview and apply workflows. Records include workflow phase, notification thread ID when applicable, HTTP method and sanitized path, request attempt, retry decision, response status, GitHub request ID, and available rate-limit metadata. Debug records are serialized across workers and remain suitable for redirected stderr; enabling them does not change the preview/report on stdout.

Debug logging is off by default. Authorization headers, authentication tokens, URL query values, response bodies, and notification content are omitted by construction. Share debug logs only after applying your normal operational review policy.

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
