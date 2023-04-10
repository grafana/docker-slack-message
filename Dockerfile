FROM golang:1.20.3 as build

WORKDIR /app
COPY . .
RUN CGO_ENABLED=0 go build -o slack-message ./...

FROM alpine:latest

COPY --from=build /app/slack-message /app/slack-message
RUN mkdir /app/outputs

CMD /app/slack-message
