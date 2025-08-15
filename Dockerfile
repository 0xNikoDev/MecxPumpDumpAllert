FROM golang:1.21 AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o MexcPumpDumpAlert .

FROM alpine:latest
RUN apk --no-cache add ca-certificates
WORKDIR /app
COPY --from=builder /app/MexcPumpDumpAlert .
COPY .env .env
CMD ["./MexcPumpDumpAlert"]