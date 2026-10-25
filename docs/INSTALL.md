# Installing Movie Night Showdown

This guide gets you from zero to a running showdown with Docker Compose. It
assumes a machine that can run Docker, and at least one place to get movies from.

## What you need first

- **Docker** with the Compose plugin (`docker compose version` should work).
- **At least one movie source.** The app needs one of these; both is better:
  - **A Jellyfin server** reachable from wherever you run this — a URL like
    `http://jellyfin.local:8096` — plus **a Jellyfin API key** (created below).
    The app uses the key server-side to read your library and fetch posters; it
    is never sent to the people swiping.
  - **A Plex Media Server** reachable from wherever you run this — a URL like
    `http://plex.local:32400` — plus **a Plex token** (found below). Plex is a
    peer of Jellyfin, not a replacement: you can run either, or both at once.
  - **A TMDB API read token**, which lets the app draw from streaming services
    instead of (or alongside) a local library. See
    [Streaming services](#streaming-services).

You do not need any of these in hand before starting. Bring the app up with
nothing configured, then add sources from the settings screen — see
[The quickest path](#the-quickest-path). Until at least one source exists, the
app redirects to a `/setup` page explaining what is missing, since there would
be no movies to deal.

### Getting a Jellyfin API key

In Jellyfin, open **Dashboard → API Keys** and create a new key. Give it a name
you'll recognize (for example, `movie-night-showdown`) and copy the value — this
becomes `JELLYFIN_API_KEY` below.

If you want the "unwatched only" filter to work, you also need to say whose watch
state counts. The settings screen lists your Jellyfin accounts and lets you pick
one, so you only need to find the raw ID when configuring by environment variable:
open **Dashboard → Users**, click the user, and copy the ID out of the browser's
address bar (`.../useredit.html?userId=<this part>`). That value becomes
`JELLYFIN_USER_ID`.

### Getting a Plex token

Plex does not have an API-key screen. Instead you copy the token your own
session already uses:

1. Open Plex Web and click any movie.
2. Open the item's **⋮** menu → **Get Info** → **View XML**.
3. A new tab opens with the item's raw XML. The address bar ends in
   `?X-Plex-Token=<value>`.
4. Copy that value. It becomes `PLEX_TOKEN`.

Two alternatives if the web UI does not cooperate:

- **From the server host.** `Preferences.xml` contains a `PlexOnlineToken`
  attribute. On a package install it is usually at
  `/var/lib/plexmediaserver/Library/Application Support/Plex Media Server/Preferences.xml`.
- **From the API.** `POST https://plex.tv/users/sign_in.xml` with your
  credentials and an `X-Plex-Client-Identifier` header returns an `authToken`.

Treat the token as a password: it grants access to your library and your
plex.tv account. Like the Jellyfin API key, the app uses it server-side only
and never sends it to a browser.

Unlike Jellyfin, no extra user ID is needed for the "unwatched only" filter — a
Plex token already identifies one user, so play state is always available.

#### Choosing which movie libraries to use

Both Jellyfin and Plex can hold several libraries, and each library you name becomes
its own movie source — so a host can run a session against your children's library
alone. Name none and every library is used, which is what an untouched deployment
does.

The easiest way to choose is the settings screen: check the connection and it lists
what your server actually has.

By environment variable, name them however you like — the display name or the
identifier:

```
JELLYFIN_LIBRARIES=Films,Kids Films
PLEX_LIBRARY_SECTIONS=Films,Kids Films
```

Names are what you know. Identifiers are what works when the app starts *before*
your media server does: a name has to be looked up on the server first, so on a cold
start the app comes up without those libraries and adds them once the server answers,
whereas an identifier needs no lookup and is registered immediately. If your app and
your media server start together and you want the sources present from the first
second, use identifiers.

To find Plex's section keys by hand:

```bash
curl -sS -H "Accept: application/json" \
  "$PLEX_URL/library/sections?X-Plex-Token=$PLEX_TOKEN" \
  | jq '.MediaContainer.Directory[] | {key, type, title}'
```

The `key` of the section whose `type` is `"movie"` is the value to use.

Jellyfin's library ids the same way — they are hexadecimal rather than numeric,
but they go in `JELLYFIN_LIBRARIES` exactly as Plex's keys go in
`PLEX_LIBRARY_SECTIONS`:

```bash
curl -sS -H "Accept: application/json" -H "X-Emby-Token: $JELLYFIN_API_KEY" \
  "$JELLYFIN_URL/Library/MediaFolders" \
  | jq '.Items[] | select(.CollectionType == "movies") | {Id, Name}'
```

`Id` is the value to use. Only folders whose `CollectionType` is `movies` are
worth naming — the app skips the rest, since a music library offered as a movie
source is one whose every query comes back empty.

## The quickest path

Start the app with nothing configured, then fill everything in from the settings
screen. You do not need any credentials in a file to begin.

**`docker-compose.yml`** — pull the pre-built image:

```yaml
services:
  showdown:
    image: ghcr.io/eiladin/movie-night-showdown:latest
    ports:
      - "8080:8080"
    environment:
      PORT: "8080"
      CACHE_DIR: /var/cache/mns
      CONFIG_FILE: /config/config.yaml
      PUBLIC_URL: http://your-server-ip:8080
    volumes:
      - config:/config
      - poster-cache:/var/cache/mns
    restart: unless-stopped

volumes:
  config:
  poster-cache:
```

The `config` volume matters. It holds the settings the app writes, including
your credentials and its setup token. Without it, every restart forgets
everything you configured.

Bring it up:

```bash
docker compose up -d
```

Confirm it's healthy:

```bash
curl -fsS localhost:8080/healthz
# {"commit":"...","status":"ok","version":"..."}
```

### Getting the setup token

Changing this server's configuration needs a token, which the app generates the
first time it starts and prints to its log:

```bash
docker compose logs showdown | grep "setup: token"
# setup: token for configuration changes: 9f2c...
```

There is no account system, so the log is the only place this appears. Whoever
can read the log is whoever deployed the app, which is the correct audience.
Keep the token to yourself: it authorizes changes to where this server points,
and a server pointed at somewhere else will send your stored credentials there.

If you lose it, stop the container, delete the `setupToken` line from
`config.yaml` in the config volume, and start it again — a new one is generated
and printed.

### Configuring it

Open `/settings` on the app, paste the setup token, and fill in whichever
sources you have. Each section has a toggle; only the sources you enable are
offered to hosts.

Credentials are stored on the server and are never sent back to the browser — a
saved one shows as a placeholder, and you replace it by typing a new value or
remove it with the button beside it.

Each section has a **Check** button. Use it. It makes the server query that source
for real and tells you what came back — the number of movies Jellyfin can see, the
name of the Plex library that will be used, or which part is wrong: an unreachable
URL and a rejected credential are reported differently, because the fix differs.
You do not have to save first, and if a credential is already stored you can leave
the field alone and check what is saved.

Checking also fills in the settings you would otherwise have to go and look up.
Jellyfin's accounts appear as a dropdown for the "unwatched only" filter, and each
service's movie libraries appear as chips you pick from. All of them are opaque
identifiers — a 32-character user id, a hexadecimal library id, a numeric section key
— so none of those fields is shown until a check has fetched the options. There is
nothing to type.

**Choosing several libraries changes what a host sees.** Each library you pick becomes
its own source, so the host's picker lists "Jellyfin — Kids Movies" beside "Netflix"
rather than one entry called "Jellyfin". That is the point: a session can be dealt
from one library alone. Pick none and every library is used, which is what a
deployment that has never touched this setting already does.

Most changes take effect immediately. Changing a movie source rebuilds the deck
sources, which ends any session in progress, so the screen asks first.

Now open `PUBLIC_URL` in a browser, start a showdown, and share the code or QR
with everyone else on the network.

### Configuring with environment variables instead

Every setting can also be supplied as an environment variable, which is how a
deployment that has never opened the settings screen is configured. See
[Configuration reference](#configuration-reference) for the full list, and read
[How settings are resolved](#how-settings-are-resolved) first — the two are not
interchangeable, and the difference will otherwise surprise you later.

## Building from source instead

If you'd rather build the image yourself, clone the repository and point Compose
at the local `Dockerfile`. The repo already ships a `docker-compose.yml` set up
for exactly this:

```bash
git clone <this-repo> movie-night-showdown
cd movie-night-showdown
docker compose up --build -d
```

Configure it the same way as the pre-built image: read the setup token from the
log and open `/settings`. If you would rather seed it from a file first, copy
`.env.example` to `.env` and fill in what you have — the shipped
`docker-compose.yml` reads it if present.

## How settings are resolved

The app owns its configuration file. The settings screen writes it, and what it
writes is what the app runs with.

Environment variables **seed** a deployment that has not saved a value for a
setting yet. Once the file sets one, the variable for it is ignored. This is the
same model Jellyfin, Plex, and the \*arr applications use, and it exists so that
saving a setting is never silently undone by a variable in a compose file.

Resolution is per key, not per source. If your file sets `plex.url` and your
environment sets `PLEX_TOKEN`, you get both.

Three things are deliberately never file-managed, because each names something
established before the process starts:

| Variable | Why |
|---|---|
| `PORT` | The listener is already bound and cannot be rebound under live connections. |
| `CACHE_DIR` | It names a path inside the container, which has to be mounted before it can be used. |
| `CONFIG_FILE` | It is how the file is found in the first place. |

The settings screen shows all three, read-only, so you can see what is in effect.
Changing one means editing your deployment and recreating the container.

### When a variable is being ignored

This is the one thing about this model that will confuse you six months from
now, so it is reported in two places.

The startup log states where every setting came from, and names any variable it
is ignoring:

```
config: plex.url = http://plex.local:32400 (config file; PLEX_URL present, ignored)
config: plex.token = *** (config file; PLEX_TOKEN present, ignored)
config: publicUrl = http://nas:8080 (environment)
config: sessionTtl = 4h (default)
```

The settings screen badges the same fields, so the conflict is visible at the
moment you are editing one.

If you want a variable to take effect again, remove the setting from the config
file — clearing the field in the settings screen does exactly that.

### Where the config file lives

`CONFIG_FILE` names it, defaulting to `./config/config.yaml`. A missing file is
not an error — a fresh deployment has saved nothing yet, and the app creates the
file the first time it writes, which is on its first start when it generates the
setup token.

What does stop the app is a path it cannot use: if the directory cannot be
created, every save would fail later, so it fails at startup instead with one
clear message naming the path.

The file is written with mode `0600` and holds your credentials in plaintext,
along with the setup token. It is not encrypted — an app that decrypts its own
configuration unattended has to keep the key where it can read it, which is
obfuscation with the cost of encryption. Do not commit it, do not paste it into
an issue, and be aware that it lands in any backup of its volume.

## Configuration reference

Every setting below can be supplied as an environment variable. Everything except
`PORT`, `CACHE_DIR` and `CONFIG_FILE` can also be set from the settings screen,
which takes precedence — see [How settings are resolved](#how-settings-are-resolved).

| Variable | Required | Purpose |
|---|---|---|
| `JELLYFIN_URL` | one of¹ | Base URL of your Jellyfin server. |
| `JELLYFIN_API_KEY` | one of¹ | Jellyfin API key. Stays server-side; never sent to clients. |
| `JELLYFIN_USER_ID` | no | Needed for the "unwatched only" filter. |
| `PLEX_URL` | one of¹ | Base URL of your Plex Media Server, including the port (usually `32400`). |
| `PLEX_TOKEN` | one of¹ | Plex authentication token. Stays server-side; never sent to clients. See [Getting a Plex token](#getting-a-plex-token). |
| `PLEX_LIBRARY_SECTIONS` | no | Comma-separated movie libraries to draw from, by name or by section key. Each becomes its own source. Unset means every library. See [Choosing which movie libraries to use](#choosing-which-movie-libraries-to-use). |
| `PLEX_LIBRARY_SECTION` | no | Deprecated singular form, still read when the plural is unset so existing deployments keep working. Use the plural. |
| `JELLYFIN_LIBRARIES` | no | Comma-separated movie libraries to draw from, by name or by library id. Each becomes its own source. Unset means every library. |
| `TMDB_READ_TOKEN` | one of¹ | TMDB v4 API Read Access Token. Unlocks streaming services as sources; without it only your local libraries are offered. Stays server-side; never sent to clients. See [Streaming services](#streaming-services). |
| `STREAMING_PROVIDERS` | no | Comma-separated list of streaming services to offer, by name or numeric TMDB provider id. Any provider TMDB tracks is accepted. Defaults to `netflix,prime,disney` **only for a deployment that has not saved streaming settings** — once the settings screen manages them, services are chosen explicitly or not at all. Ignored when `TMDB_READ_TOKEN` is unset. See [Choosing which services to offer](#choosing-which-services-to-offer). |
| `TMDB_WATCH_REGION` | no | ISO 3166-1 country code deciding which streaming services exist and what they carry. Defaults to `US`. See [Setting the region](#setting-the-region). |
| `PUBLIC_URL` | yes | The URL people use to reach the app. Used to build the join links and QR codes, so it must be reachable from their devices. |
| `SESSION_TTL` | no | How long an idle session survives. Defaults to a few hours (`4h`). |
| `PORT` | no | Port the app listens on. Defaults to `8080`. Deployment-level: not settable from the settings screen. |
| `CACHE_DIR` | no | Where posters are cached on disk. Mount a volume here to keep the cache across restarts. Deployment-level: not settable from the settings screen. |
| `CONFIG_FILE` | no | Where the settings the app writes are stored. Defaults to `./config/config.yaml`. Deployment-level: not settable from the settings screen. |

¹ The app needs at least one movie source, configured either from the settings
screen or through these variables: `JELLYFIN_URL` **and** `JELLYFIN_API_KEY` for
a Jellyfin library, `PLEX_URL` **and** `PLEX_TOKEN` for a Plex library, or
`TMDB_READ_TOKEN` for streaming services. Any combination works, and every
configured source appears in the host's source picker. With none of them set, the
app serves only its `/setup` page, which points at the settings screen.

A movie present in more than one source appears once, carrying a badge for each:
the same film in your Plex library and on Netflix is one card, not two. Sources
are matched on their TMDB id, so a Plex item its agents never matched to TMDB
cannot merge and appears as a Plex-only card.

The one that trips people up is `PUBLIC_URL`. It's the address the *phones* use,
not the address the container uses internally. If your guests reach the app at
`http://192.168.1.50:8080`, that's what belongs here — otherwise the QR code will
point somewhere their phones can't reach.

## Streaming services

Without a TMDB token the deck is drawn from your own libraries alone. Setting
`TMDB_READ_TOKEN` adds streaming services as additional sources the host can
select when starting a showdown. Netflix, Prime Video, and Disney+ are offered
by default, and any other service TMDB tracks — Hulu, Peacock, Max, and so on —
can be requested with `STREAMING_PROVIDERS`. Catalog data
comes from [TMDB](https://www.themoviedb.org/); a movie that appears both in your
library and on a streaming service is shown once, with a badge for each place it
can be watched.

To get a token, sign in to TMDB and open
[Settings → API](https://www.themoviedb.org/settings/api). Request an API key,
then copy the **API Read Access Token** (the long v4 token, not the shorter v3
key) into `TMDB_READ_TOKEN`. The token stays on the server and is never sent to
browsers.

When the token is unset, the app does not advertise streaming sources at all:
the API reports only the local libraries you configured, and that is all the
source picker shows, along with a short note about enabling the rest.

### Running without a local library

A TMDB token alone is enough: configure neither Jellyfin nor Plex and the deck is
built entirely from streaming catalogs. Two differences from a deployment that
has a library:

- **Filter options are a fixed list.** With a library, the genre and rating
  chips are enumerated from what is actually on your shelf. A streaming catalog
  is far too large to enumerate, so the picker offers a default vocabulary
  instead — the 19 genres and the six US certifications (`G`, `PG`, `PG-13`,
  `R`, `NC-17`, `NR`) that a streaming query can honor.
- **"Unwatched only" is unavailable.** It reads play state off a local server —
  a named Jellyfin user, or whoever the Plex token belongs to — and has no
  meaning for a streaming catalog.

### Choosing which services to offer

`STREAMING_PROVIDERS` is a comma-separated list of the services to offer. It is
not limited to a fixed set: **any watch provider TMDB tracks can be named**, so
if your household uses Hulu and Peacock, ask for those.

```yaml
STREAMING_PROVIDERS: hulu,peacock
```

Behavior:

- **Unset** — Netflix, Prime Video, and Disney+ are offered, so existing
  deployments are unchanged.
- **Set** — the list you give *replaces* the default entirely; it is not added
  to it. To keep Netflix alongside Hulu, name both: `netflix,hulu`.
- Whitespace around entries is trimmed, names are matched case-insensitively,
  empty entries are skipped, and duplicates collapse. `Netflix, DISNEY` is the
  same as `netflix,disney`.
- The order you list them in is the order they appear in the picker.
- Names are matched against TMDB's watch-provider list for your region, by the
  provider's name or by a dashed version of it — `hbo max` and `hbo-max` both
  find HBO Max.
- An entry that matches nothing is logged and skipped. It never fails startup,
  so a typo costs you one service rather than the whole deployment.
- With `TMDB_READ_TOKEN` unset the variable has no effect: no streaming service
  can be queried without a token.

These eight are recognized without any lookup, and keep working even if TMDB is
unreachable when the app starts: `netflix`, `prime`, `disney`, `hulu`,
`peacock`, `max`, `apple`, `paramount`. Everything else is resolved by asking
TMDB once at startup.

#### Using a numeric TMDB provider id

Instead of a name you can give the provider's numeric TMDB id. This skips name
matching entirely, which is worth doing when a service's name is ambiguous,
regional, or has changed:

```yaml
STREAMING_PROVIDERS: 15,386,1899     # Hulu, Peacock, HBO Max
```

Names and ids can be mixed in the same list (`netflix,15,peacock`).

To find an id, ask TMDB for the provider list for your region. In a browser or
with `curl`, using your read token:

```bash
curl -s -H "Authorization: Bearer $TMDB_READ_TOKEN" \
  "https://api.themoviedb.org/3/watch/providers/movie?watch_region=US" \
  | jq '.results[] | {id: .provider_id, name: .provider_name}'
```

Each entry's `provider_id` is what goes in `STREAMING_PROVIDERS`. Without `jq`,
open the same URL in a browser session signed in to TMDB and search the JSON for
the service name. The ids are stable, so this is a one-time lookup.

If you give an id TMDB does not list for your region, it is still used — the id
is all the catalog query needs — but it shows up unnamed in the picker, as
`Provider <id>`. That usually means the region is wrong rather than the id.

### Setting the region

Streaming availability is regional: which services exist, and what each one
carries, both depend on where you are. `TMDB_WATCH_REGION` takes an ISO 3166-1
country code and defaults to `US`.

```yaml
TMDB_WATCH_REGION: GB
```

It applies to both provider resolution and every catalog query, so setting it
wrong is the usual explanation for a service that resolves but returns nothing.

### Checking what resolved

The app's own `/setup` page lists the sources actually in use, so you can
confirm a name resolved the way you expected. The startup log records anything
that did not.

## Putting it behind a reverse proxy

For a real deployment you'll usually front it with a reverse proxy (Caddy,
Traefik, nginx, etc.) that terminates TLS and gives it a nice hostname. Two
things to get right:

- Set `PUBLIC_URL` to the public HTTPS address, e.g. `https://showdown.example.com`.
- Make sure the proxy forwards **WebSocket** connections — the live lobby, the
  synchronized swiping, and the match reveal all depend on the `/ws` endpoint
  staying open. Most proxies do this automatically or with one directive.

## Keeping the poster cache

The app caches poster images on disk so repeated showdowns don't re-fetch every
image from your library. Mount a volume at `CACHE_DIR` (as the examples above do) and
that cache survives restarts and upgrades. It's purely a performance nicety —
delete it any time and it simply rebuilds.

## Updating

With the pre-built image, pull the newer tag and recreate the container:

```bash
docker compose pull
docker compose up -d
```

Building from source, pull the latest code and rebuild:

```bash
git pull
docker compose up --build -d
```

## Troubleshooting

**The QR code goes to a page that won't load.** `PUBLIC_URL` is almost certainly
set to something the phones can't reach (like `localhost`). Set it to the
server's actual address on the network.

**No movies show up when filtering.** Check the source's URL and credential on
the settings screen. The health check at `/healthz` tells you the app is running,
but library queries need a credential with access to your movie library.

**I changed a variable in `docker-compose.yml` and nothing happened.** The
config file wins for any setting it has saved. The startup log says so per
setting — look for `present, ignored`:

```bash
docker compose logs showdown | grep ignored
```

Either change the setting on the settings screen instead, or clear it there so
the variable takes effect again. See
[How settings are resolved](#how-settings-are-resolved).

**My settings vanished after recreating the container.** The config volume is
not mounted, so the file the app writes went with the container. Add
`config:/config` and `CONFIG_FILE=/config/config.yaml`, as in
[The quickest path](#the-quickest-path). This also means the setup token changes
on every start.

**I lost the setup token.** Stop the container, remove the `setupToken` line from
`config.yaml` in the config volume, and start it again. A new one is generated
and printed to the log.

**Streaming services disappeared after I saved settings.** Once the settings
screen manages streaming, services are selected explicitly — the old
`netflix,prime,disney` default no longer applies. Enable streaming on the
settings screen, check the token, and tick the services you want.

**The lobby never updates or matches never fire.** That's the WebSocket
connection being dropped — check that your reverse proxy is forwarding `/ws`.
