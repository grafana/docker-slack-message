#! /usr/bin/env bash

if [ -z "${SLACK_CHANNEL}" ] || [ -z "${SLACK_TOKEN}" ] || [ -z "${IMAGE}" ]; then
    echo "Please set SLACK_CHANNEL, SLACK_TOKEN and IMAGE environment variables"
    exit 1
fi

WORKDIR="$(mktemp -d)"
trap 'rm -rf -- "${WORKDIR}"' EXIT

RUN="docker run -v "${WORKDIR}:${WORKDIR}" -w "${WORKDIR}" -e SLACK_OUTPUT_DIR="${WORKDIR}" -e SLACK_TOKEN"

# Create thread
${RUN} -e SLACK_CHANNEL -e SLACK_MESSAGE=testingroot ${IMAGE}

# Reply to thread
${RUN} -e SLACK_CHANNEL -e SLACK_MESSAGE=reply -e SLACK_THREAD_TS=$(cat ${WORKDIR}/thread-ts) ${IMAGE}
FIRST_MESSAGE_TS=$(cat ${WORKDIR}/message-ts)

# Reply to thread
${RUN} -e SLACK_CHANNEL -e SLACK_MESSAGE=reply2 -e SLACK_THREAD_TS=$(cat ${WORKDIR}/thread-ts) ${IMAGE}
SECOND_MESSAGE_TS=$(cat ${WORKDIR}/message-ts)

# Reply to thread
${RUN} -e SLACK_CHANNEL -e SLACK_MESSAGE=reply3 -e SLACK_THREAD_TS=$(cat ${WORKDIR}/thread-ts) ${IMAGE}
THIRD_MESSAGE_TS=$(cat ${WORKDIR}/message-ts)

# Edit first message
${RUN} -e SLACK_CHANNEL=$(cat ${WORKDIR}/channel-id) -e SLACK_MESSAGE=reply1-updated -e SLACK_COLOR=#ff0000 -e SLACK_UPDATE_MESSAGE_TS=${FIRST_MESSAGE_TS} ${IMAGE}

# Delete second message
${RUN} -e SLACK_CHANNEL=$(cat ${WORKDIR}/channel-id) -e SLACK_DELETE_MESSAGE_TS=${SECOND_MESSAGE_TS} ${IMAGE}
