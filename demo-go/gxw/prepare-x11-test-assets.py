#!/usr/bin/env python3
"""Prepare a relocatable, persistent Xvfb/xinit bundle from an extracted sysroot.

Place packages and their extracted files in TEST-ASSETS/x11/{packages,sysroot}.
No system files or global symlinks are installed.
"""

import hashlib
from pathlib import Path
import shutil

root = Path(__file__).resolve().parents[2] / "TEST-ASSETS" / "x11"
sysroot = root / "sysroot"
original = sysroot / "usr/bin/Xvfb"
data = original.read_bytes()
# Arch's X server hardcodes the compiler directory. Keep the original executable
# and patch only this fixed-size string in a separate test copy. Its wrapper sets
# cwd to the bundle, making ./bin/xkbcomp independent of the checkout's location.
prefix = b"/usr/bin\0"
if data.count(prefix) != 1:
    raise SystemExit("unsupported Xvfb compiler prefix; original left untouched")
relocated = root / "Xvfb-relocatable"
relocated.write_bytes(data.replace(prefix, b"./bin\0\0\0\0"))
relocated.chmod(0o755)

xkb = sysroot / "usr/share/X11/xkb"
if not xkb.exists():
    shutil.copytree("/usr/share/X11/xkb", xkb)
bindir = root / "bin"
bindir.mkdir(exist_ok=True)
for name in ("Xvfb", "xinit", "xkbcomp"):
    executable = sysroot / "usr/bin" / name
    if not executable.is_file():
        raise SystemExit(f"missing test tool: {executable}")
    command = f'exec "$root/sysroot/usr/bin/{name}" "$@"\n'
    if name == "Xvfb":
        command = 'cd "$root"\nexec "$root/Xvfb-relocatable" -xkbdir "$root/sysroot/usr/share/X11/xkb" "$@"\n'
    wrapper = bindir / name
    wrapper.write_text('#!/bin/sh\nset -eu\nroot=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)\n'
                       'export LD_LIBRARY_PATH="$root/sysroot/usr/lib${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}"\n' + command)
    wrapper.chmod(0o755)

files = sorted((root / "packages").glob("*")) + [original, relocated]
(root / "SHA256SUMS").write_text("".join(
    f"{hashlib.sha256(path.read_bytes()).hexdigest()}  {path.relative_to(root)}\n"
    for path in files if path.is_file()
))
(root / "README.md").write_text("""# Persistent X11 test tools

`bin/` contains relocatable wrappers for Xvfb, xinit and xkbcomp. `sysroot/`
retains their extracted packages, libraries and XKB data; `packages/` retains
the original Arch Linux x86-64 package archives. SHA256SUMS records the archives
and original/relocated Xvfb executables. These tools use the host's other shared
libraries and are local test assets, outside version control.

From the repository root, run `make test-gxw-x11`. The tracked runner adds this
bundle to PATH. No /usr/bin links, installed X server, or temporary tool paths
are required. Live tests still use isolated temporary displays and directories.

To regenerate wrappers after replacing/extracting packages, run:
`python3 demo-go/gxw/prepare-x11-test-assets.py`.
The original Xvfb is retained; the derived copy changes its hardcoded compiler
directory to ./bin and its wrapper sets the working directory and XKB path.
""")
print(f"Prepared persistent X11 test tools in {root}")
