# Clipferry relay. The desktop app is built with scripts/build-*.sh instead.
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /clipferry-relay ./cmd/clipferry-relay

FROM alpine:3.22
RUN adduser -D -H relay
COPY --from=build /clipferry-relay /usr/local/bin/clipferry-relay
USER relay
ENV PORT=8080
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s CMD wget -qO- http://127.0.0.1:8080/healthz || exit 1
CMD ["clipferry-relay"]
