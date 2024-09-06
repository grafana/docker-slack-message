# docker-slack-message

Very simple tool to send Slack messages. Built into a docker image

## Testing commands

```console
export SLACK_CHANNEL=...
export SLACK_TOKEN=...
export IMAGE=...

docker run -e SLACK_CHANNEL -e SLACK_TOKEN -e SLACK_MESSAGE=testingroot ${IMAGE}
docker run -e SLACK_CHANNEL -e SLACK_TOKEN -e SLACK_MESSAGE=reply -e SLACK_THREAD_TS=<fill in from previous> ${IMAGE}
docker run -e SLACK_CHANNEL -e SLACK_TOKEN -e SLACK_MESSAGE=reply2 -e SLACK_THREAD_TS=<fill in from previous> ${IMAGE}
docker run -e SLACK_CHANNEL=<fill in from previous> -e SLACK_TOKEN -e SLACK_MESSAGE=reply-updated -e SLACK_UPDATE_MESSAGE_TS=<fill in from previous> ${IMAGE}
```
