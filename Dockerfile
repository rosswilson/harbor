FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /controlplane ./cmd/controlplane && CGO_ENABLED=0 go build -trimpath -o /demo ./cmd/demo

FROM alpine:3.22 AS demo
RUN adduser -D app
USER app
COPY --from=build /demo /demo
EXPOSE 8080
ENTRYPOINT ["/demo"]

FROM alpine:3.22 AS controlplane
RUN apk add --no-cache ca-certificates
COPY --from=build /controlplane /controlplane
EXPOSE 9090 18000
ENTRYPOINT ["/controlplane"]
