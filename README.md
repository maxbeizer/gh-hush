# gh-hush

`gh-hush` is a read-only, explainable GitHub notification triage extension. It fetches notifications through the authenticated `gh` CLI session, evaluates a user-owned YAML policy in deterministic order, and reports what it would keep or recommend unsubscribing from.

Version 1 does **not** mutate GitHub.

## Install

From a release:

```bash
gh extension install maxbeizer/gh-hush
```

From a local checkout:

```bash
make build
make install-local
```

## Dry run

```bash
gh hush --config ~/.config/gh-hush/policy.yml --dry-run
```

The report includes every notification's URL, subject type, repository, notification reason, proposed action, and exact matching rules with evidence.
Progress is written to stderr while subject details are fetched, so large notification inboxes remain visibly active.

Dry-run guarantees:

- uses the authenticated `gh` session and GitHub Notifications API;
- makes no GitHub mutations, including no unsubscribe, mute, or mark-as-read calls;
- evaluates keep rules before the catch-all unsubscribe rule;
- treats missing evidence conservatively by keeping the thread and reporting the enrichment failure;
- reports an explicit message when GitHub successfully returns zero notifications; and
- fails visibly when authentication, notification fetching, configuration, or report generation fails.

## Configuration

Policy stays outside this repository. Pass an explicit path to a user-owned YAML file:

```yaml
user: YOUR-GITHUB-LOGIN
github_organization: YOUR-PRIMARY-ORGANIZATION
run_mode: ad_hoc

discussion_team_slugs:
  - YOUR-PRIMARY-ORGANIZATION/YOUR-TEAM

keep:
  external_organization_issues: true
  personally_mentioned: true
  personally_assigned: true
  individually_review_requested: true
  authored_by_user: true
  team_mentioned_discussions: true

unsubscribe:
  all_other_notifications: true

output:
  default_mode: dry_run
  include_keep_decisions: true
  include_unsubscribe_decisions: true
  include_decision_reasons: true
```

Unknown fields, missing policy fields, duplicate teams, malformed logins or team slugs, non-ad-hoc execution, and output settings that would hide decisions or reasons are rejected. The configured user must match the account authenticated by `gh auth`.

Keep flags may be set to `false` to disable that rule. The catch-all unsubscribe rule and complete explainable output are required in v1. Rules run in this order:

1. Keep issues outside `github_organization`.
2. Keep personal mentions and personal assignments.
3. Keep individual review requests; a team review request alone does not match.
4. Keep work authored by `user`.
5. Keep Discussions whose subject or latest comment contains an exact configured team mention.
6. Recommend unsubscribe for everything else.

If required subject or comment data cannot be fetched and no earlier keep rule already matched, the safety rule keeps the thread instead of guessing.

## Review manifest

A dry run can write a private, non-overwriting JSON manifest:

```bash
gh hush --config ~/.config/gh-hush/policy.yml --dry-run \
  --write-manifest manifest.json
```

The manifest contains only explicit unsubscribe recommendations, their thread IDs, policy evidence, the authenticated user, a configuration hash, a schema version, and `"reviewed": false`.

> [!WARNING]
> Applying a manifest will be destructive because it will unsubscribe from GitHub threads. `--apply-manifest` is intentionally unavailable in v1. A future implementation must require an explicitly reviewed manifest, verify the authenticated user and schema, and operate only on the thread IDs listed in that manifest. It must never classify and unsubscribe everything in one step.

There is no scheduler or recurring mode. Run `gh hush` only when you choose to triage notifications.

## Development

```bash
make build
make test
make ci
```

Releases are built for macOS, Linux, and Windows by GoReleaser when a `v*` tag is pushed.
