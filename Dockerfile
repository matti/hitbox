FROM golang:1.17-alpine

RUN apk add --no-cache \
  bash redis

RUN go install github.com/cespare/reflex@v0.3.1

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY main.go ./
RUN go build

COPY . .

ENTRYPOINT [ "/app/entrypoint.sh" ]

