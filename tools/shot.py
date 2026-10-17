"""截图检查：python3 shot.py <url-hash> <输出png> [宽] [高] [dark] [动作脚本]"""
import sys, json
from playwright.sync_api import sync_playwright
url = sys.argv[1]; out = sys.argv[2]
w = int(sys.argv[3]) if len(sys.argv) > 3 else 1366
hgt = int(sys.argv[4]) if len(sys.argv) > 4 else 860
dark = len(sys.argv) > 5 and sys.argv[5] == 'dark'
action = sys.argv[6] if len(sys.argv) > 6 else ''
tok = open('/home/claude/test/tok').read().strip()
with sync_playwright() as p:
    b = p.chromium.launch(executable_path='/opt/pw-browsers/chromium-1194/chrome-linux/chrome')
    ctx = b.new_context(viewport={'width': w, 'height': hgt}, device_scale_factor=2 if w < 600 else 1, color_scheme='dark' if dark else 'light', has_touch=w < 600, is_mobile=w < 600)
    ctx.add_init_script(f"localStorage.setItem('shuku.token', {json.dumps(tok)})")
    pg = ctx.new_page()
    logs = []
    pg.on('console', lambda m: logs.append(f'{m.type}: {m.text}'))
    pg.on('pageerror', lambda e: logs.append(f'PAGEERROR: {e}'))
    pg.goto('http://127.0.0.1:8090/' + url)
    pg.wait_for_timeout(2500)
    if action:
        exec(action)
    pg.screenshot(path=out)
    for l in logs:
        if 'error' in l.lower() or 'PAGEERROR' in l: print(l)
    b.close()
