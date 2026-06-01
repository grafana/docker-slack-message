# docker-slack-message

Very simple tool to send Slack messages. Built into a docker image

## Mentioning users who aren't in the channel

When the message tags a Slack user (`<@U...>`) who is not a member of the target
channel, the mention is otherwise silent. Set `SLACK_MENTION_MEMBERSHIP_MODE` to
act on every user tagged in the message:

- `none` (default) — do nothing, current behavior.
- `invite` — invite the tagged users to the channel. Requires the
  `channels:write.invites` (public) or `groups:write` (private) scope, and the
  bot must already be a member of the channel (otherwise the invite fails with
  `not_in_channel`).
- `notify` — DM each tagged user a link to the channel. Requires `im:write` and
  `chat:write`. The DM is always sent; membership is not checked first.

Failures here are logged but never fail the run — posting the message is the
primary success.

## Testing commands

Requires a bot token (`xoxb-...`). See Slack docs to create one: <https://api.slack.com/quickstart>.
Run the `test.sh` script.
