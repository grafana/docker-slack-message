FROM golang:1.26.1@sha256:dd25c49df34a6ec745f1dd59593478d067679e8e8fb1e44b326d8b9e2d348777 AS build

WORKDIR /app
COPY . .
RUN CGO_ENABLED=0 go build -o slack-message ./...

FROM alpine:latest@sha256:25109184c71bdad752c8312a8623239686a9a2071e8825f20acb8f2198c3f659

COPY --from=build /app/slack-message /app/slack-message
RUN mkdir /app/outputs

ENTRYPOINT ["/app/slack-message"]
