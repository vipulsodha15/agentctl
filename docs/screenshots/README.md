# Screenshots

The main `README.md` references images in this directory. Until you drop
real captures in, these paths 404 on GitHub and the README will render
broken-image icons.

## What to capture

Capture each of these from a real running agentctl install (`agentctl ui`
to open the Web UI at `http://127.0.0.1:7777`). Save as PNG at the
filename listed. 1600×1000 (logical) is a good baseline; the README will
scale them down.

| Filename | Where in the UI | What it should show |
|---|---|---|
| `01-task-board.png` | **Tasks** route, **Board** view | All four Kanban columns (Not started / Working / Done / Abandoned) with at least one card in each. This is the hero image at the top of the README — make it look good. |
| `02-session-console.png` | A session's **Console** tab | An active back-and-forth with the agent: a user message, a tool call, and an assistant response. The provider/model chip should be visible. |
| `03-task-board-stages.png` | **Tasks** route, **Board** view | Same as `01` but with a task card that has visible stage progress dots (investigate → plan → execute). Crop or annotate to highlight the stage progress. |
| `04-assembly-line-editor.png` | **Assembly lines → Edit `bug`** | The stage list with `bug-investigator`, `bug-planner`, `bug-executor`. If you're showcasing multi-provider, use `bug-multi-provider` instead so the provider chips show. |
| `05-agent-editor.png` | **Agents → Edit `bug-investigator`** | The system prompt, MCP allow-list, and tool surface fields visible. |

## How to capture

### macOS

    # Pick the window: ⌘ + Shift + 4, then Space, then click the browser window.
    # Saved to ~/Desktop by default; move into this directory.

Or, scripted with `screencapture`:

    screencapture -i -W docs/screenshots/01-task-board.png

### Linux

    # GNOME:
    gnome-screenshot -w -f docs/screenshots/01-task-board.png

    # KDE:
    spectacle -a -b -o docs/screenshots/01-task-board.png

    # Or `flameshot gui` and save manually.

### Headless / CI capture (optional)

If you want reproducible captures, drive the SPA with Playwright. Minimal
recipe:

```bash
npx playwright install chromium
cat > capture.mjs <<'EOF'
import { chromium } from 'playwright';
const browser = await chromium.launch();
const ctx = await browser.newContext({ viewport: { width: 1600, height: 1000 } });
const page = await ctx.newPage();
await page.goto('http://127.0.0.1:7777/tasks');
await page.waitForSelector('.task-board');
await page.screenshot({ path: 'docs/screenshots/01-task-board.png', fullPage: false });
await browser.close();
EOF
node capture.mjs
```

## Style guide

- **No real secrets.** Scrub API keys, OAuth tokens, repo URLs that
  identify private work, and PR titles/branches from internal projects
  before committing.
- **Use realistic but synthetic data.** A demo repo (e.g. a fork of
  `golang/example`) and a fake issue like "Fix off-by-one in pagination"
  works well. Avoid Lorem ipsum.
- **Theme.** Capture in whichever theme (light/dark) you ship by default
  so it matches what new users see.
- **DPI.** Capture at 2× (Retina) if you can; GitHub will downscale
  cleanly.
- **Crop.** Trim browser chrome (URL bar, tabs) unless the URL is
  relevant — for `01-task-board.png` it's fine to keep the
  `127.0.0.1:7777/tasks` URL visible to reinforce "this is local".
