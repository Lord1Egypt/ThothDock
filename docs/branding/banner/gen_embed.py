#!/usr/bin/env python3
"""Emit the bash arrays for thothfetch's logo-derived banners (braille, 256-colour).
Usage: gen_embed.py > snippet.sh   (needs mk.py and the icon master PNG)"""
import re
import compose, mk

def rows(cols):
    art = compose.art_lines(cols, "braille")
    w = cols
    out = []
    for line in art:
        pad = w - compose.vis(line)
        out.append(line + " " * pad)
    return out

def bash_array(name, art):
    lines = [f"{name}=("]
    for r in art:
        r = r.replace("\033", "\\e")
        assert "'" not in r and "\\" not in r.replace("\\e", "")
        lines.append(f"  $'{r}'")
    lines.append(")")
    return "\n".join(lines)

parts = []
for name, cols in (("logo_s", 22), ("logo_m", 30), ("logo_l", 60)):
    a = rows(cols)
    parts.append(bash_array(name, a) + f"\n{name}_w={cols}")
print("\n".join(parts))
