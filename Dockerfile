FROM golang:1.27.3-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG SERVICE
RUN CGO_ENABLED=0 go build -trimpath -o /service ./cmd/${SERVICE}

FROM alpine:3.22
RUN adduser -D -u 10001 pulse
COPY --from=build /service /service
USER pulse
ENTRYPOINT ["/service"]
