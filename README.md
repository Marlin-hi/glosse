# Glosse

Kollaborativer Text mit Rand-Kommentaren.

Ein editierbares HTML-Dokument steht im Zentrum. Am Rand schweben Kommentar-Karten, die per Text-Selektion am markierten Text hängen, farbig nach Autor. Antworten bilden flache Threads. Edit und Delete sind Autor-beschränkt. Mika-Design mit Chamfer-Ecken, Glass-Cards, Drift-Kometen, JetBrains Mono.

Gebaut als Werkzeug für Visions- und Strategie-Papiere, bei denen drei bis fünf Menschen konkrete Sätze angreifen sollen, nicht frei assoziieren.

## Wofür

- Ein Draft, mehrere Mitleser
- Kommentare sitzen am konkreten Satz, nicht in einem separaten Chat
- Write-Token für Autoren, Read-Token für kommentierende Mitleser (dürfen trotzdem kommentieren)
- Markdown-Export zurück in den Vault, wenn die Runde durch ist

## Was es nicht ist

- Kein Real-Time-Editor (kein CRDT, kein Cursor-Awareness, letzter Save gewinnt)
- Kein Versionsverlauf für Nutzer (aber automatische Backup-Kopien in `history/`)
- Kein Presence-System

## Stack

- Go-Server (ein Binary, `//go:embed` für HTML)
- JSON-Storage: `document.html` + `comments.json` + `history/`
- Auth: Bearer-Token (Read/Write)
- Frontend: eine HTML-Datei, kein Build-Schritt, keine Dependencies außer JetBrains Mono über Bunny Fonts

## Schnellstart lokal

```bash
go build -o glosse .
GLOSSE_TOKEN_READ=read123 \
GLOSSE_TOKEN_WRITE=write456 \
GLOSSE_DIR=./data \
GLOSSE_TITLE="Mein Dokument" \
GLOSSE_ACCENT=halo \
./glosse
```

Dann im Browser `http://localhost:3041` öffnen, Write-Token eingeben.

## Konfiguration

| ENV | Pflicht | Default | Beschreibung |
|---|---|---|---|
| `GLOSSE_TOKEN_READ` | ja | — | Lese-Token, darf lesen und kommentieren |
| `GLOSSE_TOKEN_WRITE` | ja | — | Schreib-Token, darf Dokument editieren und jedes Kommentar löschen |
| `GLOSSE_DIR` | nein | `/var/lib/glosse` | Verzeichnis für document.html, comments.json, history/ |
| `GLOSSE_PORT` | nein | `3041` | HTTP-Port |
| `GLOSSE_TITLE` | nein | `Glosse` | Titel oben im Login und im Browser-Tab |
| `GLOSSE_SUBTITLE` | nein | — | Untertitel (optional, im Frontend noch nicht genutzt) |
| `GLOSSE_ACCENT` | nein | `halo` | Farbschema: `halo`, `aurora`, `ember`, `flux`, `moss` |
| `GLOSSE_MARLIN_NAME` | nein | — | Kommaseparierte Namen, die automatisch Halo als Autor-Farbe bekommen. Nützlich, wenn eine Person im Team "reserviert" ist. |

## Akzent-Farben

Jede Glosse hat ein Akzent-Paar aus der Mika-Palette. Der Akzent färbt Login-Button, Hauptmarkierungen, Kometen-Drift.

| Schema | Farben | Gefühl |
|---|---|---|
| `halo` | Cornflower → Ice | ruhig, reflektiv |
| `aurora` | Cyan → Mint | kühl, technisch |
| `ember` | Orange → Gold | warm, menschlich |
| `flux` | Rose → Orchid | kreativ, lebendig |
| `moss` | Green → Lime | Wachstum, Erfolg |

Autoren bekommen aus den vier Nicht-Akzent-Farben eine feste Farbe per Namens-Hash. Die eine reservierte Halo-Farbe ist per `GLOSSE_MARLIN_NAME` einstellbar.

## Deploy auf einen Server (systemd + nginx + Let's Encrypt)

1. Binary bauen: `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags="-s -w" -o glosse .`
2. Auf Server kopieren: `/opt/glosse-<name>/glosse`
3. Env-Datei anlegen: `/etc/glosse-<name>.env` (chmod 600)
4. systemd-Unit: siehe `deploy/systemd.template`
5. nginx-Config: siehe `deploy/nginx.template`
6. certbot: `certbot certonly --webroot -w /var/www/html -d <subdomain>`
7. `systemctl enable --now glosse-<name>` + `systemctl reload nginx`

Mehrere Glosse-Instanzen auf demselben Server sind möglich, solange jede einen eigenen Port und ein eigenes Datenverzeichnis hat.

## API

Alle Endpoints außer `/` und `/health` brauchen `Authorization: Bearer <token>`.

- `GET /content` → HTML-Fragment (aktuelles Dokument)
- `PUT /content` → neues HTML speichern (Write-Token)
- `GET /markdown` → Markdown-Export
- `GET /comments` → JSON-Array aller Kommentare
- `POST /comments` → neuen Kommentar anlegen (Body: JSON mit author, colorScheme, paragraphId oder parentId, anchorText, text)
- `PUT /comments/:id` → Kommentar-Text ändern (nur Autor, oder Write-Token via `X-Author`-Header)
- `DELETE /comments/:id` → Kommentar löschen (nur Autor, oder Write-Token, kaskadiert auf Antworten)

## Lizenz

MIT. Baut auf Mika Design System (Marlin Klag).
