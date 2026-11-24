FROM golang:1.27.4@sha256:3102e2c7eeee6e16de01ca3f35c56434696f70702a30b3d564b0244560981bb0 AS build

WORKDIR /app
COPY . .
RUN CGO_ENABLED=0 go build -o slack-message ./...

FROM gcr.io/distroless/static:nonroot@sha256:8ce6dd87e9823c5b48dcfb2c8604714ccb351ce59607327bcc4957448da279b5

WORKDIR /app/outputs
WORKDIR /app
COPY --from=build /app/slack-message /app/slack-message

ENTRYPOINT ["/app/slack-message"]
