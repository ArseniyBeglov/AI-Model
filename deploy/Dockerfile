ARG GO_IMAGE=golang:1.25.7
ARG RUNTIME_IMAGE=alpine:3.21

FROM ${GO_IMAGE} AS builder

ARG CI_JOB_TOKEN=""

WORKDIR /src

COPY go_base/application /src/go_base/application
COPY autotest_ai/ai-model /src/autotest_ai/ai-model

WORKDIR /src/autotest_ai/ai-model

RUN if [ -n "$CI_JOB_TOKEN" ]; then \
      printf "machine gitlab.services.mts.ru login gitlab-ci-token password %s\n" "$CI_JOB_TOKEN" > /root/.netrc ; \
    fi

RUN go mod download
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/ai-model ./cmd/assistant

FROM ${RUNTIME_IMAGE}

RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app

COPY autotest_ai/ai-model/deploy/entrypoint.sh /entrypoint.sh
COPY --from=builder /out/ai-model /app/ai-model

RUN chmod +x /entrypoint.sh /app/ai-model

ENTRYPOINT ["/entrypoint.sh"]
