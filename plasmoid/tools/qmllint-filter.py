#!/usr/bin/env python3
# SPDX-License-Identifier: GPL-3.0-or-later
#
# Filter qmllint output for a plasmoid. Plasma injects i18n()/i18nc() into every
# applet's QML context at runtime, so qmllint cannot see them and reports each
# call as an unqualified access. Everything else is printed; the exit status is
# 1 when anything is left, so `make lint-plasmoid` fails on a real finding.

import re
import sys

PATTERN = re.compile(r"^(Warning|Error): (\S+?):(\d+):(\d+): (.*)")
INJECTED = re.compile(r"i18n[cp]?\(")

left = 0
cache = {}
for line in sys.stdin:
    m = PATTERN.match(line)
    if not m:
        continue
    kind, path, ln, col, msg = m.groups()
    if path not in cache:
        with open(path, encoding="utf-8") as f:
            cache[path] = f.read().splitlines()
    src = cache[path][int(ln) - 1]
    if INJECTED.match(src[int(col) - 1:]):
        continue
    print(f"{kind}: {path}:{ln}:{col}: {msg}\n    {src.strip()}")
    left += 1

sys.exit(1 if left else 0)
