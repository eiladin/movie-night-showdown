<div align="center">

<img src="docs/screenshots/hero.png" alt="Movie Night Showdown" width="200" />

# Movie Night Showdown

**Stop arguing about what to watch. Swipe until everyone agrees.**

</div>

---

You know the drill. Four people, one couch, a library full of movies, and forty
minutes of "I don't know, what do *you* want to watch?" before somebody gives up
and puts on the same show you've all seen six times.

Movie Night Showdown is a small self-hosted app that ends that stalemate. It
plugs into the Jellyfin server you already run, turns your library into a deck of
movie posters, and lets everyone in the room swipe through it at the same time —
left for no, right for yes, from their own phone. The instant a single movie
collects a yes from *everyone*, every screen in the room lights up with the
winner and a burst of confetti. Decision made. Popcorn time.

No accounts, no apps to install, no cloud. People join by scanning a QR code or
typing a four-character session code, and the whole thing runs on your network
in a single container.

## How a showdown works

One person starts the session. They pick a name, and the app spins up a room
with a shareable code.

<div align="center">
<img src="docs/screenshots/01-landing.png" alt="Start or join a showdown" width="300" />
</div>

The host then narrows the field. Maybe tonight is a comedy night, or strictly
PG so the kids can watch, or only films from this century that nobody's seen yet.
A live preview shows exactly how many movies made the cut before anyone starts
swiping.

<div align="center">
<img src="docs/screenshots/02-host.png" alt="Filter the library by genre, year, and rating" width="300" />
</div>

Everyone else joins by scanning the QR code or punching in the session code — no
sign-up, no download, just a link. The lobby fills up in real time as people
arrive, and the host decides how many "yes" votes it takes to call a match
(usually everyone in the room).

<div align="center">
<img src="docs/screenshots/03-lobby.png" alt="The lobby with a join QR code and live roster" width="300" />
</div>

Then the swiping starts. Everyone gets the same shuffled deck of posters and goes
at their own pace. Yes, no, undo if your thumb slips. Nobody sees how anyone else
voted — only the quiet running tally of how close the room is to agreeing.

<div align="center">
<img src="docs/screenshots/04-swipe.png" alt="Swiping through the movie poster deck" width="300" />
</div>

And when a movie finally gets a yes from everyone, it's over. Every device in the
room jumps to the same winning poster at the same moment, confetti and all.

<div align="center">
<img src="docs/screenshots/05-result.png" alt="It's a match — the winning movie with confetti" width="300" />
</div>

Not every showdown ends in a clean sweep, and that's fine. If the deck runs out
before the room agrees — or the host decides everyone's swiped enough and calls
it — the session drops to a leaderboard of the movies that came closest, ranked
by how many yes votes each one drew. The host taps one to crown the winner, and
it takes over every screen just like a match would.

## Why you'd want it

It's yours. Everything runs on your own hardware against your own Jellyfin
library — no third-party service ever sees what you watch, and your Jellyfin API
key never leaves the server. It's frictionless for guests, because "scan this
code" is the entire onboarding story; nobody makes an account to pick a movie on
your couch. And it ships as one container that sits next to Jellyfin and does one
job well.

It's the kind of thing you set up once and quietly rely on every Friday night.

Optionally, it can look beyond your own shelves. Add a TMDB API read token and
the host can also draw from streaming services, so the deck covers what you can
stream as well as what you own. Netflix, Prime Video, and Disney+ come
configured; name any other service TMDB tracks — Hulu, Peacock, Max — and it is
offered too. A movie available in both places shows up once, badged with
everywhere it can be watched.
Without a token the app is Jellyfin-only, and nothing about streaming appears in
the UI. A token on its own works too, if you want a showdown with no local
library at all. See [docs/INSTALL.md](docs/INSTALL.md#streaming-services).

Start it with nothing configured and it won't leave you guessing: it points you
at a built-in setup page that spells out the environment variables for each of
those three ways to run it.

## Get it running

If you already run Jellyfin with Docker, you're a compose file and two
environment variables away from your first showdown. The full walkthrough —
example `docker-compose.yml`, how to get a Jellyfin API key, and reverse-proxy
notes — lives in **[docs/INSTALL.md](docs/INSTALL.md)**.

## Under the hood

The backend is a single Go binary: it manages sessions, talks to Jellyfin, keeps
every device in sync over WebSockets, and proxies (and caches) poster images so
your Jellyfin key stays server-side. The frontend is a React app that's embedded
right into that binary, so there's nothing else to deploy. Configuration is a
handful of environment variables, all documented in the install guide.
