FROM golang:1.25.4@sha256:698183780de28062f4ef46f82a79ec0ae69d2d22f7b160cf69f71ea8d98bf25d AS build

WORKDIR /app
COPY . .
RUN CGO_ENABLED=0 go build -o slack-message ./...

FROM alpine:latest@sha256:51183f2cfa6320055da30872f211093f9ff1d3cf06f39a0bdb212314c5dc7375

RUN apk add --no-cache jq curl
COPY --from=build /app/slack-message /app/slack-message
RUN mkdir /app/outputs

ENTRYPOINT ["/app/slack-message"]
