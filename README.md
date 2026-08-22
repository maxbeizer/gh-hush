# gh-hush

`gh-hush` is a safe, explainable GitHub notification triage extension. It fetches notifications through the authenticated `gh` CLI session, evaluates a user-owned YAML policy in deterministic order, and previews what it will keep or unsubscribe from before making any changes.

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

## Usage

Run without flags to generate a read-only preview and, when running in an interactive terminal, confirm whether to apply the proposed unsubscriptions:

```bash
gh hush
```

The confirmation defaults to **No**. The exact decisions shown in the preview are applied; notifications are not fetched and classified a second time.

To preview without prompting or making changes:

```bash
gh hush --dry-run
```

To apply the preview without an interactive prompt, such as from automation, explicitly confirm it:

```bash
gh hush --confirm
```

A no-flag invocation with non-interactive input remains read-only and tells you to re-run with `--confirm`. `--dry-run` and `--confirm` cannot be combined.

The report includes every notification's URL, subject type, repository, notification reason, proposed action, and exact matching rules with evidence. Progress is written to stderr while subject details are fetched, so large notification inboxes remain visibly active.

Preview guarantees:

- uses the authenticated `gh` session and GitHub Notifications API;
- makes no GitHub mutations while generating the preview;
- evaluates keep rules before the catch-all unsubscribe rule;
- fetches only evidence required by enabled rules and conservatively keeps a thread when that required evidence is unavailable;
- reports an explicit message when GitHub successfully returns zero notifications;
- shows command help when the default configuration file is missing; and
- fails visibly when authentication, notification fetching, report generation, or other configuration errors occur, including a missing explicitly provided configuration.

## Configuration

Policy stays outside this repository. By default, gh-hush reads:

```text
$XDG_CONFIG_HOME/gh-hush/config.yml
```

When `XDG_CONFIG_HOME` is unset, it reads `~/.config/gh-hush/config.yml`. Override that location explicitly when needed:

```bash
gh hush --config /path/to/another-policy.yml --dry-run
```

The configuration schema is:

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

Keep flags may be set to `false` to disable that rule. The catch-all unsubscribe rule and complete explainable output are required. Rules run in this order:

1. Keep issues outside `github_organization`.
2. Keep personal mentions and personal assignments.
3. Keep individual review requests; a team review request alone does not match.
4. Keep work authored by `user`.
5. Keep Discussions whose subject or latest comment contains an exact configured team mention.
6. Propose unsubscribing from everything else.

If subject or comment data required by an enabled rule cannot be fetched and no earlier keep rule already matched, the safety rule keeps the thread instead of guessing. Failures for evidence that no enabled rule needs do not trigger a safety keep. An enabled Discussion team-mention rule with an empty `discussion_team_slugs` list requires no Discussion evidence because no team can match.

There is no scheduler or recurring mode. Run `gh hush` only when you choose to triage notifications.

## Development

```bash
make build
make test
make ci
```

Releases are built for macOS, Linux, and Windows by GoReleaser when a `v*` tag is pushed.
