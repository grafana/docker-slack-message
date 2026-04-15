FROM golang:1.26.2@sha256:5f3787b7f902c07c7ec4f3aa91a301a3eda8133aa32661a3b3a3a86ab3a68a36 AS build

WORKDIR /app
COPY . .
RUN CGO_ENABLED=0 go build -o slack-message ./...

FROM alpine:latest@sha256:25109184c71bdad752c8312a8623239686a9a2071e8825f20acb8f2198c3f659

COPY --from=build /app/slack-message /app/slack-message
RUN mkdir /app/outputs

ENTRYPOINT ["/app/slack-message"]
