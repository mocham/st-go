FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -tags 'backend_web netgo osusergo terminal_tabs' -o /out/st-web .
RUN CGO_ENABLED=0 go -C demo-go/file-browser build -trimpath -o /out/file-browser .
RUN CGO_ENABLED=0 go -C demo-go/gols build -trimpath -o /out/gols .
RUN CGO_ENABLED=0 go -C demo-go/govim build -trimpath -o /out/govim .
RUN CGO_ENABLED=0 go -C demo-go/opencode-tui build -trimpath -o /out/opencode-tui .

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends bash ca-certificates curl \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --create-home --uid 1000 --shell /bin/bash terminal \
    && mkdir /workspace && chown terminal:terminal /workspace
COPY --from=build /out/ /usr/local/bin/
USER terminal
ENV HOME=/home/terminal SHELL=/bin/bash
WORKDIR /workspace
EXPOSE 8080
ENTRYPOINT ["st-web"]
