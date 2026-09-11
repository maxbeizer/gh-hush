# gh-hush

`gh-hush` is a safe, explainable GitHub notification triage extension. It fetches unread notifications through the account authenticated by `gh`, evaluates a user-owned policy, and previews every decision before making changes.

## Demo

https://github.com/user-attachments/assets/dd441762-bfd9-4b45-a9da-22f8ac2de5d1


## Install

```bash
gh extension install maxbeizer/gh-hush
```

## Usage

```bash
gh hush             # preview; prompt with default No when fully interactive
gh hush --dry-run   # preview only
gh hush --confirm   # preview and apply without prompting
gh hush --quiet     # no preview; prompt, then print one concise result to stderr
gh hush --quiet --confirm # no preview or prompt; apply and print one concise result
gh hush --quiet --dry-run # no mutation; print only the eligible target count
gh hush --debug     # add request/workflow diagnostics on stderr
gh hush --version   # print the installed release version
```

A no-flag invocation is preview-only unless stdin, preview output, and prompt output are all interactive terminals. Redirected or piped execution requires `--confirm` to mutate GitHub. `--dry-run` and `--confirm` are mutually exclusive.

Quiet mode suppresses the preview, progress, timings, safe skip details, and application summary. Its confirmation prompt and final result are written to stderr. Without `--confirm`, `--quiet` requires interactive stdin and stderr and fails with guidance rather than falling back to preview-only behavior. Declining prints `No changes made.`; no eligible targets prints `Done: no notification updates needed.` Classification and apply failures remain nonzero and include the underlying actionable error. `--quiet` and `--debug` are mutually exclusive.

The complete preview unconditionally shows every discovered notification's URL, subject type, repository, reason, proposed action, and matching policy evidence. Authentication, notification listing, configuration, and report-generation failures return nonzero without mutation. A required preview evidence failure is reported and conservatively safety-keeps that notification; it is not an eligible mutation target. Declining confirmation, having no eligible targets, a missing target record, a target that is no longer unread, and a genuine newly matching keep rule return zero.

### Elapsed timings

gh-hush reports concise, human-readable elapsed times on stderr for the major phases so a large inbox makes it clear where time went, without printing one line per request or notification:

```text
authenticated and listed 1120 unread notifications in 2.1s
classified 1120/1120 notifications in 18.4s
generated preview report in 120ms
Application summary
  targets:      1120 notifications
  unsubscribed: 1120 succeeded, 0 failed
  marked Done:  1120 succeeded, 0 failed
  elapsed:      42.7s
total runtime: 63.4s (excludes interactive confirmation wait)
```

Durations use whole milliseconds under a second, one decimal of seconds under a minute (`18.4s`), and minutes above that (`1m02.3s`). Very fast phases render as `0ms`. The apply summary always carries its aggregate `elapsed` alongside the existing safety counters. Total runtime excludes the time spent waiting for interactive confirmation, as its label states. Failed or canceled runs still show timings for the phases that completed; the returned error identifies the interrupted phase, and a canceled apply reports its partial `elapsed`. Fine-grained per-request timing belongs to `--debug`. Timing instrumentation never changes worker bounds, operation ordering, retries, or cancellation, and workers do not write timing output directly.

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

### Upgrading from v0.2.x

No configuration migration is required. Existing valid v0.2.x configurations remain valid in v0.3.x because `watched_repositories` is optional and defaults to no watched repositories.

Upgrade the extension, validate the existing configuration, and preview the resulting decisions before applying anything:

```bash
gh extension upgrade gh-hush
gh hush validate-config
gh hush --dry-run
```

If you use a non-default configuration path, pass `--config PATH` to the validation and preview commands. `init-config` is intended for new installations and refuses to overwrite an existing file. To opt into watched-repository protection after upgrading, add the desired entries under [`watched_repositories`](#watched-repositories).

On the first run, create a valid conservative starter configuration with your explicit identity values:

```bash
gh hush init-config --user YOUR-GITHUB-LOGIN --github-organization YOUR-PRIMARY-ORGANIZATION
gh hush init-config --user YOUR-GITHUB-LOGIN --github-organization YOUR-PRIMARY-ORGANIZATION \
  --team YOUR-PRIMARY-ORGANIZATION/YOUR-TEAM
```

`--team` is optional and repeatable. Initialization enables every documented keep rule, including the external-organization protection, and the catch-all hush action. It does not contact GitHub or infer your user, organization, or teams. It creates parent directories and a user-readable-only file, refuses to overwrite any existing path, prints the created path, and tells you to review and validate the policy. Use `--config PATH` with `init-config` to create a non-default file. A normal run with no default config exits with the exact path and initialization command instead of showing generic help.

Every normal run validates the complete configuration before contacting GitHub and exits with a descriptive error if it is invalid. To check it independently, run:

```bash
gh hush validate-config
gh hush validate-config --config PATH
```

The machine-readable [JSON Schema](config.schema.json) documents every field and can be configured in editors that support YAML schemas. A synchronization test fails when the Go configuration type and the published schema differ, so contributors must update both together.

```yaml
user: YOUR-GITHUB-LOGIN
github_organization: YOUR-PRIMARY-ORGANIZATION

team_slugs:
  - YOUR-PRIMARY-ORGANIZATION/YOUR-TEAM

keep:
  external_organization_issues: true
  personally_mentioned: true
  personally_assigned: true
  individually_review_requested: true
  active_team_review_requested_pull_requests: true
  authored_by_user: true
  team_mentioned_discussions: true

watched_repositories:
  YOUR-ORGANIZATION/YOUR-WATCHED-REPOSITORY:
    open_pull_requests: true
    open_issues: true
  ANY-OWNER/ANOTHER-REPOSITORY:
    all_notifications: true

hush:
  all_other_notifications: true
```

This schema is intentionally incompatible with earlier versions: `run_mode`, `unsubscribe`, and the entire `output` section are unknown fields and are rejected. Every keep boolean is required (and may be `false`); `hush.all_other_notifications` is required and must be `true`. Complete previews are unconditional. The configured user must match the authenticated account.

Keep rules protect:

1. notifications from repositories outside `github_organization`, for every subject type;
2. `reason: mention`;
3. `reason: assign` or a current personal assignment;
4. a current individual pull-request review request;
5. an open pull request with a current review request for a configured team;
6. work authored by `user`; and
7. Discussions containing an exact configured team mention in the body or anywhere in the complete paginated comment history.

### Watched repositories

`watched_repositories` is optional and adds protection for repositories you want to follow even when nothing is directed at you. Each key is an `owner/repo` name matched case-insensitively, and may belong to any owner. Capabilities are opt-in: an omitted capability is disabled, and every entry must enable at least one.

| Capability | Protects |
| --- | --- |
| `all_notifications` | Every notification in the repository, regardless of subject type or state. No subject request is required. |
| `open_pull_requests` | Notifications whose subject is an open pull request, including drafts. Merged and closed pull requests are not protected. |
| `open_issues` | Notifications whose subject is an open Issue. |
| `open_discussions` | Notifications whose subject is a Discussion that is not closed. Answered and locked Discussions are still protected. |

Watched repositories are strictly additive: a match keeps the notification, and a non-match falls through to the keep rules above unchanged. When a capability needs the subject's state and that state is unavailable or unrecognized, the notification is conservatively safety-kept.

Closed and merged pull requests do not match the team-review keep rule and proceed through normal policy evaluation. Required evidence failures, including an unavailable or unrecognized pull-request state, conservatively safety-keep a notification. Discussion team mentions found in historical comments continue to protect the Discussion until it is manually resolved.

Only `Issue`, `PullRequest`, `Discussion`, `Commit`, `Release`, and `CheckSuite` are eligible for the catch-all hush action. Unsupported, unknown, sensitive, administrative, and security-related subject types are safety-kept.

> The historical configuration key `external_organization_issues` is retained, but its protection now intentionally applies to all notification subject types.

## Development

```bash
make build
make test
make ci
make lint
```
