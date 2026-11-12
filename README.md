# st-go

st-go is a Go terminal with X11, Linux framebuffer, and web-browser backends.
It is a port of [suckless st](https://st.suckless.org/).
Its Linux framebuffer backend has a window compositor,
touchpad/mouse gesture, IME, and shared clipboard builtin,
and can serve as a Linux framebuffer Desktop Environment.
Its web backend allows for operating on a remote server.

## Build and run

```sh
make st          # X11: BINARIES/st
make st-fb       # Linux framebuffer/virtual console: BINARIES/st-fb
make st-fb-min   # Minimal framebuffer profile: BINARIES/st-fb-min
make st-web      # CGO-free web server/client: BINARIES/st-web

./BINARIES/st
./BINARIES/st-fb -fb /dev/fb0 -tty /dev/tty -mouse auto
./BINARIES/st-web -port 8080
```

## Documentation

- [Architecture and source layout](platform_neutral/README.md)
- [Building, tests, and packaging](platform_neutral/BUILDING.md)
- [Shared frontend, configuration, rendering, and limits](platform_neutral/frontend/README.md)
- [Framebuffer](platform_dependent/fb/README.md), [X11](platform_dependent/x11/README.md), and [web](platform_dependent/web/README.md) backends
- [Terminal protocols](platform_neutral/stterminal/README.md)
