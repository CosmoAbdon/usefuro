# SPA dist/ folders are committed, so no node stage is needed.
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags "-s -w" -o /out/furo-server ./server/cmd/furo-server

FROM alpine:3.21
RUN apk add --no-cache ca-certificates && adduser -D -H furo
COPY --from=build /out/furo-server /usr/local/bin/furo-server
USER furo
VOLUME /var/lib/furo
EXPOSE 443 7835
ENTRYPOINT ["furo-server"]
CMD ["serve", "--config", "/etc/furo/config.yml"]
