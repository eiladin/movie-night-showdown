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
  - **A TMDB API read token**, which lets the app draw from streaming services
    instead of (or alongside) a local library. See
    [Streaming services](#streaming-services).

If you start the app with neither configured, it says so: the log prints what is
missing, and the app itself redirects to a `/setup` page listing the environment
variables for each option. Nothing else works until one is set, since there
would be no movies to deal.

### Getting a Jellyfin API key

In Jellyfin, open **Dashboard → API Keys** and create a new key. Give it a name
you'll recognize (for example, `movie-night-showdown`) and copy the value — this
becomes `JELLYFIN_API_KEY` below.

If you want the "unwatched only" filter to work, you also need a user ID. Open
**Dashboard → Users**, click the user whose watch state should count, and copy
the ID out of the browser's address bar (`.../useredit.html?userId=<this part>`).
That value becomes `JELLYFIN_USER_ID`.

## The quickest path

Create a folder for the deployment with two files in it.

**`.env`** — your configuration. Fill in your own values:

```bash
JELLYFIN_URL=http://jellyfin.local:8096
JELLYFIN_API_KEY=your-jellyfin-api-key
JELLYFIN_USER_ID=your-jellyfin-user-id   # optional; needed for "unwatched only"
TMDB_READ_TOKEN=your-tmdb-v4-read-token  # optional; enables streaming sources
STREAMING_PROVIDERS=netflix,prime,disney # optional; any TMDB watch provider, by name or id
TMDB_WATCH_REGION=US                     # optional; defaults to US
PUBLIC_URL=http://your-server-ip:8080
SESSION_TTL=4h
```

**`docker-compose.yml`** — pull the pre-built image:

```yaml
services:
  showdown:
    image: registry.eiladin.xyz/movie-night-showdown:latest
    ports:
      - "8080:8080"
    env_file: .env
    environment:
      PORT: "8080"
      CACHE_DIR: /var/cache/mns
    volumes:
      - poster-cache:/var/cache/mns
    restart: unless-stopped

volumes:
  poster-cache:
```

Then bring it up:

```bash
docker compose up -d
```

Confirm it's healthy:

```bash
curl -fsS localhost:8080/healthz
# {"commit":"...","status":"ok","version":"..."}
```

Now open `PUBLIC_URL` in a browser, start a showdown, and share the code or QR
with everyone else on the network.

## Building from source instead

If you'd rather build the image yourself, clone the repository and point Compose
at the local `Dockerfile`. The repo already ships a `docker-compose.yml` set up
for exactly this:

```bash
git clone <this-repo> movie-night-showdown
cd movie-night-showdown
cp .env.example .env      # then edit it with your Jellyfin details
docker compose up --build -d
```

## Configuration reference

Everything is configured through environment variables.

| Variable | Required | Purpose |
|---|---|---|
| `JELLYFIN_URL` | one of¹ | Base URL of your Jellyfin server. |
| `JELLYFIN_API_KEY` | one of¹ | Jellyfin API key. Stays server-side; never sent to clients. |
| `JELLYFIN_USER_ID` | no | Needed for the "unwatched only" filter. |
| `TMDB_READ_TOKEN` | one of¹ | TMDB v4 API Read Access Token. Unlocks streaming services as sources; without it only Jellyfin is offered. Stays server-side; never sent to clients. See [Streaming services](#streaming-services). |
| `STREAMING_PROVIDERS` | no | Comma-separated list of streaming services to offer, by name or numeric TMDB provider id. Any provider TMDB tracks is accepted. Defaults to `netflix,prime,disney`. Ignored when `TMDB_READ_TOKEN` is unset. See [Choosing which services to offer](#choosing-which-services-to-offer). |
| `TMDB_WATCH_REGION` | no | ISO 3166-1 country code deciding which streaming services exist and what they carry. Defaults to `US`. See [Setting the region](#setting-the-region). |
| `PUBLIC_URL` | yes | The URL people use to reach the app. Used to build the join links and QR codes, so it must be reachable from their devices. |
| `PORT` | no | Port the app listens on. Defaults to `8080`. |
| `SESSION_TTL` | no | How long an idle session survives. Defaults to a few hours (`4h`). |
| `CACHE_DIR` | no | Where posters are cached on disk. Mount a volume here to keep the cache across restarts. |

¹ The app needs at least one movie source. Set `JELLYFIN_URL` **and**
`JELLYFIN_API_KEY` for a local library, or `TMDB_READ_TOKEN` for streaming
services, or all three for both. With none of them set, the app serves only its
`/setup` page.

The one that trips people up is `PUBLIC_URL`. It's the address the *phones* use,
not the address the container uses internally. If your guests reach the app at
`http://192.168.1.50:8080`, that's what belongs here — otherwise the QR code will
point somewhere their phones can't reach.

## Streaming services

By default the deck is drawn from your Jellyfin library alone. Setting
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
the API reports only Jellyfin, and the source picker shows only Jellyfin along
with a short note about enabling the rest.

### Running without a Jellyfin library

A TMDB token alone is enough: leave `JELLYFIN_URL` and `JELLYFIN_API_KEY` unset
and the deck is built entirely from streaming catalogs. Two differences from a
deployment that has a library:

- **Filter options are a fixed list.** With a library, the genre and rating
  chips are enumerated from what is actually on your shelf. A streaming catalog
  is far too large to enumerate, so the picker offers a default vocabulary
  instead — the 19 genres and the six US certifications (`G`, `PG`, `PG-13`,
  `R`, `NC-17`, `NR`) that a streaming query can honor.
- **"Unwatched only" is unavailable.** It reads a Jellyfin user's play state and
  has no meaning for a streaming catalog.

### Choosing which services to offer

`STREAMING_PROVIDERS` is a comma-separated list of the services to offer. It is
not limited to a fixed set: **any watch provider TMDB tracks can be named**, so
if your household uses Hulu and Peacock, ask for those.

```yaml
STREAMING_PROVIDERS: hulu,peacock,max
```

Behavior:

- **Unset** — Netflix, Prime Video, and Disney+ are offered, so existing
  deployments are unchanged.
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
image from Jellyfin. Mount a volume at `CACHE_DIR` (as the examples above do) and
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

**No movies show up when filtering.** Double-check `JELLYFIN_URL` and
`JELLYFIN_API_KEY`. The health check at `/healthz` tells you the app is running,
but library queries need a valid key with access to your movie library.

**The lobby never updates or matches never fire.** That's the WebSocket
connection being dropped — check that your reverse proxy is forwarding `/ws`.
