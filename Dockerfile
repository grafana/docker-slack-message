FROM golang:1.26.4@sha256:68cb6d68bed024785b69195b89af7ac7a444f27791435f98647edff595aa0479 AS build

WORKDIR /app
COPY . .
RUN CGO_ENABLED=0 go build -o slack-message ./...

FROM alpine:latest@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b

COPY --from=build /app/slack-message /app/slack-message
RUN mkdir /app/outputs

ENTRYPOINT ["/app/slack-message"]
