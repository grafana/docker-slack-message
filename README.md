# docker-slack-message

Very simple tool to send Slack messages. Built into a docker image

## Server mode

By default the container sends a single message (configured via environment
variables) and exits. Set `SLACK_SERVER_MODE=true` to instead run a persistent
HTTP server that posts a Slack message for each incoming `POST` request.

- The bot token is **only** read from the `SLACK_TOKEN` environment variable. It
  is never accepted in the request body (sending a `token` field returns
  `400`).
- `GITHUB_SLACK_MAPPING_ENDPOINT` is also read from the environment (shared
  across requests).
- `SLACK_SERVER_ADDR` sets the listen address (default `:8080`).
- `GET /healthz` returns `200` for health checks.

Every other field is supplied per request as JSON. `channel` is required:

```bash
docker run -p 8080:8080 -e SLACK_SERVER_MODE=true -e SLACK_TOKEN="xoxb-..." docker-slack-message

curl -sS -X POST http://localhost:8080/ \
  -H 'Content-Type: application/json' \
  -d '{
        "channel": "C0123456789",
        "title": "Deploy finished",
        "message": "All good :rocket:",
        "color": "#008000"
      }'
# => {"channel_id":"C0123456789","message_ts":"...","thread_ts":"..."}
```

Supported body fields: `channel`, `title`, `message`, `context`, `color`,
`thread_ts`, `update_message_ts`, `delete_message_ts`, `also_send_to_channel`,
`gh_user`, `enable_mentions`, `mention_membership_mode`. The response is JSON
with `channel_id`, `message_ts` and `thread_ts`.

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
