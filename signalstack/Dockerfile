FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /slinkd-server ./server/

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=build /slinkd-server /usr/local/bin/slinkd-server
EXPOSE 8080
ENTRYPOINT ["slinkd-server"]
