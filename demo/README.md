# Shell terminal demos

These Bash clients use the [terminal's text protocols](../platform_neutral/stterminal/README.md),
not native display connections. Run the examples from the repository root.

## Image/PDF viewer

`image-viewer.sh` uses the DCS display DSL. Left/Right navigate files, Up/Down
change PDF pages, and `q`/Escape quits:

```sh
./BINARIES/st -e ./demo/image-viewer.sh
./BINARIES/st -e ./demo/image-viewer.sh -d /path/to/images
./BINARIES/st -e ./demo/image-viewer.sh -p 3 /path/to/docs
```

tmux reserves DCS and does not forward these frames by default, so image
display requires passthrough or a direct st-go session.

## File browser

```sh
./BINARIES/st -e ./demo/file-browser.sh /some/path
./BINARIES/st -e ./demo/file-browser.sh --hidden /some/path
```

The [file-browser guide](../demo-go/file-browser/README.md) covers navigation,
editable path completion, shell/rename prompts, icons, backend-aware openers,
and framebuffer video for both the Bash and Go implementations.

See also the [Go applications](../demo-go/README.md) and
[build guide](../platform_neutral/BUILDING.md).
