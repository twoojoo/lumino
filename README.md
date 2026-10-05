# Kubeon

Native Kubernetes IDE in the spirit of Lens — no Electron, no web stack.

- **UI**: [Gio](https://gioui.org) (GPU immediate-mode, pure Go): Linux (Wayland/X11),
  Windows (Direct3D 11), macOS (Metal).
- **Engine**: [k9s](https://github.com/derailed/k9s) v0.51.0 backend packages
  (`client`, `watch` informers, `dao`, `model`, `render`) vendored in
  `third_party/k9s` with rewritten import paths (Apache-2.0, see its LICENSE).

## Features

| Area | What |
|---|---|
| Navigation | Lens-like sidebar (Workloads, Config, Network, Storage, Access Control, CRDs by group), context & namespace pickers |
| Tables | k9s renderers/colorers, live informer updates, virtualized rows (2k rows ≈ 3 ms/frame), click-to-sort, k9s filters (regex, `-f` fuzzy, `!` inverse, `-l` labels), wide mode |
| Details | Describe + YAML drawer, in-place edit & apply |
| Dock | Log streaming (multi-container, follow, search, wrap, timestamps), pod shell (exec + VT emulator) |
| Actions | Delete (with force), scale, rollout restart, port-forward (`pf` lists/stops) |
| Nodes | Cordon / uncordon, drain (ignore DaemonSets, delete emptyDir, force) |
| Logs+ | Multi-pod streams for deployments/sts/ds/jobs/services with per-pod tags, JSON pretty-print (level/msg/fields), level filter, download to `~/Downloads` |
| Secrets | Decoded data tab, per-key reveal/copy, copy all as `.env`, download `.env` (mode 600) |
| Helm | Releases, history (enter), values (user/computed), rollback, uninstall |
| XRay | k9s dependency tree for deploy/sts/ds/rs/svc/pods as collapsible tree or pannable graph |
| Safety | Per-context policies: **protected** (auto-detected for names like `prod`/`production`/`prd`/`live`) adds a red frame, a PRODUCTION badge and type-the-name confirmation for every change, and asks before opening a port-forward tunnel (pinned tunnels restored on connect are listed in the notification bell); **read-only** blocks edits, deletes, scaling, shells and assistant actions — port-forwards stay allowed: they change nothing in the cluster (Kubernetes RBAC on `pods/portforward` still applies) |
| Selection | Ctrl+click, Shift+click, `space`, Ctrl+A; bulk delete/restart/cordon with one confirmation; right-click context menu with every action for the row (or the selection) |
| Workspace | Tabs (Ctrl+T / Ctrl+W / Ctrl+Tab), bookmarks of resource+namespace+filter (`m`, sidebar section, `:bm`), port-forward manager with pinned forwards restored on connect and open-in-browser |
| Appearance | Dark and light themes (title bar toggle, `:theme`), preferences panel (Ctrl+, or gear) |
| Notifications | Health watcher (CrashLoop/ImagePull/OOM, node NotReady, deployment degraded/rollout complete) → desktop notifications (D-Bus on Linux, Notification Center on macOS, toasts on Windows) + in-app bell |
| Overview | Nodes, pods by status, deployments health, CPU/memory requests (+usage with metrics-server), warning events |
| Keyboard | k9s style: `:` / Ctrl+K command palette (`po`, `deploy`, `ns demo`, `ctx …`, `pf`), `/` filter, `j/k`, `enter`, `l` logs, `p` previous logs, `s` shell, `d` describe, `y` yaml, `e` edit, `shift+f` port-forward, `r` restart (drain on nodes), `c`/`u` cordon/uncordon, `x` xray (decode on secrets), `v` helm values, `a` assistant, `b`/`esc` back, `ctrl+d` delete, `w` wide, `n` namespace, `0` all namespaces; palette: `xray deploy`, `helm`, `notify` |

## Assistant (AI)

Right-side panel (`a`, the **Assistant** button in the title bar, or `:ai <question>`)
that troubleshoots the cluster with read-only tools: overview, list, YAML, describe,
events, logs (multi-pod, previous, grep) and XRay. Changes are only **proposed**
(`scale`, `restart`, `delete`, `apply`, `cordon`, `uncordon`) and run after you approve
them in a confirmation dialog.

| Provider | API | Key |
|---|---|---|
| Anthropic (Claude, default `claude-opus-5-5`) | official `anthropic-sdk-go`, streaming, server-side refusal fallback | `$ANTHROPIC_API_KEY` / `ant auth login` |
| OpenAI, Google Gemini, Mistral, Groq, OpenRouter, DeepSeek, xAI | OpenAI Chat Completions (`openai-go`) | `$OPENAI_API_KEY`, `$GEMINI_API_KEY`, … |
| Ollama, LM Studio, vLLM, llama.cpp, LocalAI (local) | OpenAI-compatible endpoint | none |

Settings (gear icon) store provider, model, endpoint and optionally a key in
`~/.config/kubeon/ai.json` (mode 600); models can be listed from the endpoint. The
assistant can be disabled per kube context.

Privacy: Secret values are never sent (data/stringData replaced, keys kept); env
vars with sensitive names, bearer tokens, passwords in URLs/logs, JWTs, cloud and
VCS tokens and PEM private keys are redacted from every tool output.

Local models need reliable tool calling (e.g. Llama 3.1 8B+, Qwen 2.5 7B+); very
small models may describe tool calls instead of making them — Kubeon executes
well-formed JSON calls written as text, but larger models give far better answers.

## Install

Release packages are published in the public repository
[twoojoo/kubeon-releases](https://github.com/twoojoo/kubeon-releases/releases)
(this source repository is private), or build them yourself — see Packaging:

| OS | Packages | Notes |
|---|---|---|
| Linux amd64 (arm64: `make package-linux` on an arm64 host) | `.AppImage`, `.deb`, `.rpm`, `.tar.gz` | glibc ≥ 2.34 (Ubuntu 22.04+, Debian 12+, Fedora 35+); Wayland or X11 |
| Windows 10+ amd64/arm64 | portable `.zip` (`kubeon.exe`) | unsigned: SmartScreen asks once ("More info → Run anyway") |
| macOS 12+ (universal) | `.dmg`, `.zip` | ad-hoc signed unless built with a Developer ID: first launch via right-click → Open, or `xattr -dr com.apple.quarantine /Applications/Kubeon.app` |

Kubeon reads `$KUBECONFIG` / `~/.kube/config` like kubectl. Started from a desktop
launcher (Finder, Dock, app menu) it loads the login shell environment first, so
exec credential plugins (`aws`, `gke-gcloud-auth-plugin`, `kubelogin`, …) on your
shell `PATH` work. `kubeon --version` prints the version.

### Updates

Release builds check the latest release of `twoojoo/kubeon-releases` daily (Preferences → Updates, or *Check
now*). A newer version shows in the status bar; *Update now* downloads it,
verifies it and replaces this copy in place, then restarts:

| Installed as | Update |
|---|---|
| AppImage, `tar.gz` binary, portable Windows `kubeon.exe`, macOS `Kubeon.app` | in place, when the location is writable |
| `.deb` / `.rpm` (in `/usr`) | release page link: update with the package manager |

Every download must match `SHA256SUMS.txt`, and that file must carry a valid
ed25519 signature (`SHA256SUMS.txt.sig`) from the Kubeon release key whose
public half is built into the app (`internal/update`). Unsigned or foreign
releases are never installed. CI signs releases with the secret
`UPDATE_SIGNING_KEY` (`go run ./scripts/signsums keygen <file>` creates a key
pair; keep the private key safe — losing it means existing installs can no
longer update themselves). Dev builds never check for updates.
`KUBEON_UPDATE_URL` points the check at another release endpoint.

### Third-party licenses

Every package ships `THIRD_PARTY_NOTICES.txt` (`make notices`): the license of
each linked dependency, the vendored k9s (Apache-2.0) and Gio (Unlicense)
with the list of Kubeon's modifications, and the MPL-2.0 source note.

## Window chrome

Kubeon draws its own title bar (brand, namespace, centered command search,
window controls). Gio is vendored in `third_party/gio` with small patches
(`third_party/_gio-patches`, re-apply with `scripts/vendor-gio.sh`):

- Linux/Wayland: drag the title bar of a maximized window to restore & move it,
  double-click to maximize / restore, right-click for the compositor window menu,
  window mode stays in sync with compositor actions (Super+↑, drag-to-top, dock restore)
- macOS: native traffic lights over the custom title bar, double-click zooms
- Windows: native caption behaviour (snap, double-click, system menu);
  Direct3D 11 falls back to the WARP software rasterizer without a GPU (VMs, RDP)

## Build & run

```sh
make build            # release binary ./kubeon (uses $KUBECONFIG / ~/.kube/config like kubectl)
./kubeon --context my-ctx --namespace default --resource deploy
```

Linux system deps: Wayland/EGL/xkbcommon (present). For X11 + Vulkan support install
`libx11-dev libx11-xcb-dev libxkbcommon-x11-dev libxcursor-dev libxfixes-dev libvulkan-dev`
and build with `make build GIO_TAGS=`.

## Packaging

Everything lands in `dist/`; the version comes from `git describe` (override with `VERSION=`).

```sh
make icons            # packaging/icons (png/ico/icns) + internal/ui/assets/logo.png from packaging/logo-source.png
make package-linux    # Docker (Ubuntu 22.04 + full Gio deps): AppImage, deb, rpm, tar.gz for the host arch
make package-windows  # cross-compiled from any OS (pure Go): zips for amd64 + arm64, icon/manifest/version via go-winres
make package-macos    # on a Mac: universal Kubeon.app (arm64+amd64), dmg, zip
```

macOS signing/notarization is optional: `MACOS_SIGN_IDENTITY="Developer ID Application: …"`
and `MACOS_NOTARY_PROFILE=<notarytool keychain profile>`. In CI set the secrets
`MACOS_CERT_P12` (base64), `MACOS_CERT_PASSWORD`, `MACOS_NOTARY_APPLE_ID`,
`MACOS_NOTARY_TEAM_ID`, `MACOS_NOTARY_PASSWORD`.

CI (`.github/workflows/ci.yml`) runs unit tests, the kind integration suite with
headless UI screenshots, builds every package on Linux, Windows and macOS
runners with a launch smoke test, and on a `v*` tag publishes them, with a
signed `SHA256SUMS.txt`, as a release of the public `twoojoo/kubeon-releases`
repository. Secrets: `UPDATE_SIGNING_KEY` (release signing key) and
`RELEASES_TOKEN` (fine-grained token, Contents read/write on
`kubeon-releases` only). `packaging/releases-repo/README.md` is that
repository's README.

### Upstream k9s

`.github/workflows/upstream-k9s.yml` runs daily:

- keeps the **"k9s upstream watch"** issue up to date with what k9s `master`
  changed in the vendored packages since the vendored release (commits,
  per-package diffstat, files kubeon patches or overlays);
- when a **new k9s release** is out, re-vendors it, re-applies
  `third_party/_k9s-patches` and the overlays, runs `go mod tidy`, build, unit
  and kind integration tests, and opens a pull request (draft if anything
  failed) with the report — or an issue, if Actions may not open PRs. Run it
  by hand with a tag or branch (`ref`) to try one early.

Locally: `scripts/k9s-upstream-report.sh [tag|master]` prints the same report;
`K9S_REF=<tag> make vendor-k9s` re-vendors (default: the version in
`third_party/k9s/UPSTREAM`).

## Development (safe by construction)

Dev builds (`-tags dev`) are **locked to a disposable kind cluster**: they only accept a
kubeconfig inside a `.dev/` directory and contexts named `kind-kubeon*`; client-go caches go
to `.dev/cache`. Real clusters configured on the machine are never touched.

```sh
make dev-up           # kind cluster "kubeon-dev" → .dev/kubeconfig (+ fixtures; STRESS=1 adds 2000 configmaps)
make run              # dev build against the kind cluster
make test             # unit tests
make test-int         # backend integration + headless UI tests; screenshots in .dev/shots/
make dev-down
make vendor-k9s       # re-vendor k9s (K9S_REF=vX.Y.Z), overlays in third_party/_k9s-overlay
```

## Layout

```
cmd/kubeon            entry point, flags, logging (~/.cache/kubeon/kubeon.log)
internal/backend      facade over k9s: Cluster, TableWatch snapshots, actions, logs, exec, port-forward, overview
internal/ui           Gio app: table, sidebar, drawer, dock (logs/terminal), picker/palette, overview, icons
internal/safety       kubeconfig/context guard for dev builds
internal/desktop      OS integration: open URLs, login-shell env import, Wayland cursor settings
internal/notify       desktop notifications (D-Bus / Notification Center / Windows toasts)
packaging/            icons, Linux desktop entry + Dockerfile + nfpm, macOS Info.plist, Windows resources
scripts/package-*.sh  release packaging per OS (make package-linux|windows|macos)
third_party/k9s       vendored k9s engine (generated by scripts/vendor-k9s.sh — do not edit by hand)
```
