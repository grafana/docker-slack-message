FROM golang:1.27.3@sha256:e519d3b0a5b285e5aa53ab9eef02bc6bbb9cb04608389b743a136a9ce75f0d1e AS build

WORKDIR /app
COPY . .
RUN CGO_ENABLED=0 go build -o slack-message ./...

FROM alpine:latest@sha256:3f4d6b32e26703c6082839234f43769d5046b3ac7c910033911d1e71d8cd630a

COPY --from=build /app/slack-message /app/slack-message
RUN mkdir /app/outputs

ENTRYPOINT ["/app/slack-message"]
