import sys, json, pathlib
from playwright.sync_api import sync_playwright
A = "/home/lordegypt/AndroidThothTerm/wt-nextgen/garden-common/src/main/assets/lan"
name, cols, rows, out = sys.argv[1], int(sys.argv[2]), int(sys.argv[3]), sys.argv[4]
text = pathlib.Path(f"cand-{name}.ansi").read_text().replace("\n", "\r\n")
prompt = "\x1b[38;5;123mthoth\x1b[0m@\x1b[38;5;123mthothterm\x1b[0m:~$ "
html = f"""<!doctype html><meta charset=utf-8><link rel=stylesheet href="file://{A}/xterm.css">
<body style="margin:0;background:#090D12"><div id=t style="padding:8px"></div>
<script src="file://{A}/xterm.js"></script><script>
const term = new ThothXterm.Terminal({{cols:{cols},rows:{rows},fontFamily:"DejaVu Sans Mono, monospace",fontSize:15,
  theme:{{background:"#090D12",foreground:"#DCE8EE",cursor:"#18E7FF"}}}});
term.open(document.getElementById("t"));
term.write({json.dumps(prompt + "thothfetch" + chr(13) + chr(10))} + {json.dumps(text)} + {json.dumps(chr(13)+chr(10)+prompt)});
</script>"""
pathlib.Path("/tmp/claude-1000/xt.html").write_text(html)
with sync_playwright() as p:
    b = p.chromium.launch(args=["--allow-file-access-from-files"])
    pg = b.new_page(viewport={"width": cols * 10 + 40, "height": rows * 20 + 40})
    pg.goto("file:///tmp/claude-1000/xt.html"); pg.wait_for_timeout(800)
    pg.locator("#t").screenshot(path=out); b.close()
