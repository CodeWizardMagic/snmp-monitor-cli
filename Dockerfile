FROM golang:1.22-alpine AS builder
WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY main.go ./
RUN CGO_ENABLED=0 GOOS=linux go build -o snmp-monitor ./main.go

FROM alpine:3.20
WORKDIR /app
COPY --from=builder /app/snmp-monitor /app/snmp-monitor

ENTRYPOINT ["/app/snmp-monitor"]
CMD ["--host=host.docker.internal", "--community=public", "--interval=1"]
