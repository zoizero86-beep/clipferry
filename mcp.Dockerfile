# Standalone Clipferry MCP server (stdio). It serves the clipboard history
# the Clipferry app keeps on a computer; in a container without that history
# it starts with an empty one, which is enough for MCP directories to check
# that the server runs and lists its tools.
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /clipferry-mcp ./cmd/clipferry-mcp

FROM alpine:3.22
RUN adduser -D clipferry
COPY --from=build /clipferry-mcp /usr/local/bin/clipferry-mcp
USER clipferry
ENTRYPOINT ["clipferry-mcp"]
