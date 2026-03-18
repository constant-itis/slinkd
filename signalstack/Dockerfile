FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /signalstack-server ./server/

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=build /signalstack-server /usr/local/bin/signalstack-server
EXPOSE 8080
ENTRYPOINT ["signalstack-server"]
