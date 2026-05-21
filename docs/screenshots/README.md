# Screenshots

The main `README.md` references images in this directory.

## What's here

| Filename | Where in the UI | What it shows |
|---|---|---|
| `01-task-board.png` | **Tasks** route, **Board** view | All four Kanban columns (Not started / Working / Done / Abandoned). Hero image at the top of the README. |
| `02-assembly-lines.png` | **Assembly lines** route | List of assembly lines with one selected; the right pane shows its ordered stages. |
| `03-new-assembly-line.png` | **Assembly lines → New assembly line** | The authoring form for a new assembly line — name, description, and the stage list. |
| `04-sessions.png` | **Sessions** route | The Sessions view. |

If you replace these, keep the filenames (the main `README.md` references
them by name). 1600×1000 (logical) is a good baseline; the README will
scale them down.

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
