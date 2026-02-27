FROM golang:1.22-alpine AS build

WORKDIR /src

COPY apps/go_team_api/go.mod apps/go_team_api/go.sum ./
RUN go mod download

COPY apps/go_team_api/ ./
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/go-team-api .

FROM alpine:3.20

RUN apk add --no-cache ca-certificates \
  && adduser -D -u 10001 app

WORKDIR /app

COPY --from=build /out/go-team-api /app/go-team-api
COPY apps/go_team_api/web /app/web

USER app

EXPOSE 18081

# Override in platform env if needed.
ENV GO_TEAM_API_ADDR=0.0.0.0:18081

CMD ["/app/go-team-api"]
