FROM golang:1.24

WORKDIR /app

COPY . ./

RUN go mod download

# Build
RUN CGO_ENABLED=0 GOOS=linux go build -o /app/basement

EXPOSE 8081

# Run
ENTRYPOINT ["/app/basement"]

