# synk в контейнере: статический бинарь без cgo, образ без оболочки,
# процесс не root. Данные — том /data (accounts.db, учётки, фото).
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
ARG COMMIT=unknown
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X github.com/hobby-me/money-tracks-sync/internal/version.Version=${VERSION} -X github.com/hobby-me/money-tracks-sync/internal/version.GitCommit=${COMMIT}" \
      -o /out/synk ./cmd/synk \
 && mkdir -p /out/data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/synk /synk
# Папка данных заводится в образе с владельцем nonroot: именованный том
# наследует её права при первом создании, иначе /data принадлежал бы root,
# и процесс не открыл бы базу («unable to open database file»).
COPY --from=build --chown=nonroot:nonroot /out/data /data
ENV SYNK_DATA=/data \
    SYNK_LISTEN=0.0.0.0:8090 \
    SYNK_METRICS_LISTEN=127.0.0.1:8091
VOLUME ["/data"]
EXPOSE 8090
USER nonroot:nonroot
ENTRYPOINT ["/synk"]
CMD ["serve"]
