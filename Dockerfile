# Builder stage
FROM golang:1.26 AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . ./

ARG VERSION=""
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath \
    -ldflags "-X github.com/valri11/basement/cmd.version=${VERSION}" \
    -o /app/basement

# Runtime stage
FROM alpine:3.21

RUN apk add --no-cache ca-certificates

COPY --from=builder /app/basement /usr/local/bin/basement

EXPOSE 8080

ENTRYPOINT ["basement"]
