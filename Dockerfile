FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /copilot2api .

FROM busybox:latest
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /copilot2api /copilot2api
ENV HOME=/root
ENV COPILOT2API_HOST=0.0.0.0
ENV COPILOT2API_PORT=7777
ENV COPILOT2API_CONTROL_PORT=7778
EXPOSE 7777 7778
ENTRYPOINT ["/copilot2api"]
