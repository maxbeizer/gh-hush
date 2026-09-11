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
gh hush --debug     # add request/workflow diagnostics on stderr
gh hush --version   # print the installed release version
```

A no-flag invocation is preview-only unless stdin, preview output, and prompt output are all interactive terminals. Redirected or piped execution requires `--confirm` to mutate GitHub. `--dry-run` and `--confirm` are mutually exclusive.

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

### Upgrading from v0.3.x

v0.4.0 replaces the flat `keep` booleans with `version: 3`: an explicit identity, a terminal default, and an ordered list of rules evaluated first-match-wins. Older files are rejected with guidance, so migrate before the next run:

```bash
gh extension upgrade gh-hush
gh hush migrate-config            # print the equivalent version 3 policy
gh hush migrate-config --write    # rewrite in place, keeping a .bak backup
gh hush validate-config
gh hush --dry-run
```

Migration is mechanical: each enabled keep switch and each `watched_repositories` entry becomes an explicit rule with the same protection, in the same precedence order. Review the result: rules are now yours to reorder, rename, narrow, and extend. If you use a non-default configuration path, pass `--config PATH` to every command above.

On the first run, create a valid conservative starter configuration with your explicit identity values:

```bash
gh hush init-config --user YOUR-GITHUB-LOGIN --github-organization YOUR-PRIMARY-ORGANIZATION
gh hush init-config --user YOUR-GITHUB-LOGIN --github-organization YOUR-PRIMARY-ORGANIZATION \
  --team YOUR-PRIMARY-ORGANIZATION/YOUR-TEAM
```

`--team` is optional and repeatable. Initialization writes a commented rule set reproducing the protections gh-hush shipped before v3, plus the `hush` default. It does not contact GitHub or infer your user, organization, or teams. It creates parent directories and a user-readable-only file, refuses to overwrite any existing path, prints the created path, and tells you to review and validate the policy. Use `--config PATH` with `init-config` to create a non-default file. A normal run with no default config exits with the exact path and initialization command instead of showing generic help.

Every normal run validates the complete configuration before contacting GitHub and exits with a descriptive error if it is invalid. To check it independently, run:

```bash
gh hush validate-config
gh hush validate-config --config PATH
```

The machine-readable [JSON Schema](config.schema.json) documents every field, including the recursive condition type, and can be configured in editors that support YAML schemas. A test validates the shipped starter policy and representative invalid documents against both the schema and the runtime parser, so contributors must update both together.

```yaml
version: 3

identity:
  user: YOUR-GITHUB-LOGIN
  organization: YOUR-PRIMARY-ORGANIZATION
  teams:
    - YOUR-PRIMARY-ORGANIZATION/YOUR-TEAM

defaults:
  action: hush              # terminal fallback when no rule matches
  on_missing_evidence: keep # safety posture when GitHub evidence is unavailable

rules:
  - name: keep work outside my organization
    action: keep
    when:
      repository:
        owner_not: YOUR-PRIMARY-ORGANIZATION

  - name: keep work directed at me
    action: keep
    when:
      any:
        - reason: [mention, assign, author]
        - assignee: me
        - review_requested: me

  - name: keep my team's active reviews
    action: keep
    when:
      all:
        - subject_type: [PullRequest]
        - state: open
        - review_requested_team: my_teams

  - name: watch a repository I follow
    action: keep
    when:
      all:
        - repository:
            any_of: [YOUR-ORGANIZATION/YOUR-WATCHED-REPOSITORY, YOUR-ORGANIZATION/dependency-*]
        - any:
            - all: [{subject_type: [PullRequest]}, {state: open}]
            - all: [{subject_type: [Issue]}, {state: open}]
            - all: [{subject_type: [Discussion]}, {state_not: closed}]

  - name: hush stale subscriptions
    action: hush
    when:
      all:
        - reason: [subscribed]
        - age:
            older_than: 30d
```

Evaluation happens in four layers, and precedence is visible in the file:

1. **Identity.** `identity` resolves `me` and `my_teams`. The configured user must match the authenticated account.
2. **Safety.** Only `Issue`, `PullRequest`, `Discussion`, `Commit`, `Release`, and `CheckSuite` are eligible for hushing. Unsupported, unknown, sensitive, administrative, and security-related subject types are safety-kept before any rule runs, and no rule can defeat that.
3. **Rules.** The ordered list is evaluated top to bottom and the first match wins, the same precedence model as firewalls, `.gitignore`, and routing tables. The matching rule's `name` is the evidence shown in the preview, so reports explain decisions in your own words.
4. **Default.** `defaults.action` is the terminal fallback when no rule matches.

When a rule needs GitHub evidence that cannot be fetched, `defaults.on_missing_evidence: keep` conservatively keeps the notification and reports the failure; `hush` treats the indeterminate rule as a non-match, continues, and still reports the failure. Evidence is fetched lazily and at most once per notification, and only for the predicates actually evaluated, so a rule that classifies from the notification alone costs no extra requests.

### Condition vocabulary

A condition is data, not an expression language. Sibling keys of a mapping form an implicit `all`, and conditions compose with `any`, `all`, and `not`. An omitted `when` matches every notification, which makes the rule an unconditional catch-all.

| Predicate | Matches on |
| --- | --- |
| `repository` | `owner`, `owner_not`, or `any_of` with `owner/repo` names and globs such as `github/dependency-*`. A bare value or list is shorthand for `any_of`. Matching is case-insensitive. |
| `subject_type` | `PullRequest`, `Issue`, `Discussion`, `Commit`, `Release`, `CheckSuite`. |
| `state` / `state_not` | `open`, `closed`, `locked`, `merged`, `draft`, `answered`. A locked Discussion counts as open until it has been closed. `merged` and `draft` apply to pull requests; `answered` applies to Discussions. |
| `reason` | GitHub's notification `reason`, for example `mention`, `assign`, `author`, `review_requested`. |
| `author`, `assignee`, `review_requested` | A login or `me`. `assignee` and `review_requested` apply only to Issues and pull requests. |
| `review_requested_team` | A team slug or `my_teams`. Team slugs match only within the notification's own owner. |
| `mentions_user`, `mentions_team` | Exact `@`-mentions, with an optional `search` scope of `body` and/or `comments`; both are searched by default. `mentions_team` slugs match only within the notification's own owner. Comment search covers the complete paginated history. |
| `age` | `older_than: 30d`, `newer_than: 7d`, in Go durations plus a `d` day suffix. Thresholds are exclusive: a notification exactly `30d` old does not match `older_than: 30d`. |

Adding a new protection is now a rule you write rather than a new configuration field: `state: closed`, a repository glob, and an age threshold all parse today.

## Development

```bash
make build
make test
make ci
make lint
```
