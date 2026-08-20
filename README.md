# torrent-indexer

[![](https://dcbadge.limes.pink/api/server/7wqNywmpQW)](https://discord.gg/7wqNywmpQW)
[![Sponsor](https://img.shields.io/badge/Sponsor-❤-ff69b4?style=for-the-badge&logo=github)](https://github.com/sponsors/felipemarinho97)

This is a simple torrent indexer that can be used to index torrents from HTML pages. It is written in Golang and uses Redis as a cache.

## Test it

Visit [https://torrent-indexer.darklyn.org/](https://torrent-indexer.darklyn.org/) to test it.

## Supported sites

- [comando-torrents](https://comando.la/)
- [bludv](https://bludv2.xyz/)
- [torrent-dos-filmes](https://torrentdosfilmes-v2.xyz/)
- [starck-filmes](https://www.starckfilmes-v20.com/)
- [rede-torrent](https://redetorrent.com/)
- [vaca-torrent](https://vaqueirofilmes.com/)
- [erai-raws](https://www.erai-raws.info/)
- [Nyaa.si](https://nyaa.si/) (anime torrent discovery)
- [Anime Tosho NEW](https://animetosho.xyz/) (Brazilian Portuguese subtitle verification)

## Deploy

If you have Docker + docker-compose installed, you can deploy it using the following command:

```bash
curl -s https://raw.githubusercontent.com/Erickfb/torrent-indexer/main/docker-compose.yml > docker-compose.yml
docker-compose up -d
```

The server will be available at [http://localhost:8080/](http://localhost:8080/).

Docker image: `ghcr.io/erickfb/torrent-indexer:latest`

## Configuration

You can configure the server using the following environment variables:
  
- `PORT`: (optional) The port that the server will listen to. Default: `7006`
- `METRICS_PORT`: (optional) The port that the metrics server will listen to. Default: `8081`
- `LOG_LEVEL`: (optional) The log level. Default: `1` (info). 
  - Possible values: `0` (debug), `1` (info), `2` (warn), `3` (error)
- `LOG_FORMAT`: (optional) The log format. Can be "json" or default to console logger.
- `FLARESOLVERR_ADDRESS`: (optional) The address of the FlareSolverr instance, including scheme and port, e.g. `http://192.168.6.10:8191`. If only a host/IP is provided, port `8191` and `http://` are assumed. Default: `N/A`
- `FLARESOLVERR_TIMEOUT_SECONDS`: (optional) Timeout for flaresolverr requests. Default: `30`
- `FLARESOLVERR_POOL_SIZE`: (optional) Number of FlareSolverr sessions in the pool. Default: `5`
- `REQUEST_TIMEOUT_MILLISECONDS`: (optional) Timeout for external scraping requests. Default: `5000`
- `MEILISEARCH_ADDRESS`: (optional) The address of the MeiliSearch instance. Default: `N/A`
- `MEILISEARCH_KEY`: (optional) The API key of the MeiliSearch instance. Default: `N/A`
- `REDIS_HOST`: (optional) The address of the Redis instance. Default: `localhost`
- `REDIS_PASSWORD`: (optional) The password of the Redis instance. Default: `N/A`
- `SHORT_LIVED_CACHE_EXPIRATION` (optional) The expiration time of the short-lived cache in duration format. Default: `30m`
    - This cache is used to cache homepage or search results.
    - Example: `30m`, `1h`, `1h30m`, `1h30m30s`
- `LONG_LIVED_CACHE_EXPIRATION` (optional) The expiration time of the long-lived cache in duration format. Default: `7d`
    - This cache is used to store the torrent webpages (posts). You can set it to a higher value because the torrent pages are not updated frequently.

### Extra Configuration
- `FALLBACK_TITLE_ENABLED`: (optional) Mark title fallbacks as unsafe by prefixing `"[UNSAFE] "` when the magnet does not provide a display name and the page title is used instead. Default: `false`
    - The API always returns a non-empty `title` when `original_title` is available because Prowlarr/Sonarr require release titles.
- `MAGNET_METADATA_API_ENABLED`: (optional) Enable the magnet metadata API. (deploy instrucitons [here](https://github.com/felipemarinho97/magnet-metadata-api)) Default: `false`
- `MAGNET_METADATA_API_ADDRESS`: (optional) The address of your magnet metadata API. Default: `N/A`
- `MAGNET_METADATA_API_TIMEOUT_SECONDS`: (optional) The timeout for the magnet metadata API requests in seconds. Default: `10`
- `INDEXER_<NAME>_URL`: (optional) Set a custom URL for the indexer. Where the "NAME" will be always uppercase indexer key with underscores. ex: `INDEXER_DODO_FILMES_URL=https://my-proxied-dodo-url.org`
    - `comando_torrents` supports both `INDEXER_COMANDO_TORRENTS_URL` and the legacy `INDEXER_COMANDO_URL`.
    - `erai_raws` defaults to `https://www.erai-raws.info/` and supports `INDEXER_ERAI_RAWS_URL`.
- `INDEXER_BLUDV_APPEND_TRACKERS`: (optional) Append a short, stable tracker list to BLUDV magnet links while preserving the original `xt` first. Useful for Transmission setups that do not resolve hash-only magnets reliably. Default: `false`.
- `INDEXER_ERAI_RAWS_RSS_URL`: (optional, recommended) Full authenticated Erai-raws RSS URL. Use this if your Erai-raws account provides a personal RSS/token URL.
- `ERAI_RAWS_AUTH_TOKEN`: (optional) Authentication token for Erai-raws feeds.
- `ERAI_RAWS_AUTH_QUERY_PARAM`: (optional) Query parameter name for `ERAI_RAWS_AUTH_TOKEN`. Default: `token`.
- `ERAI_RAWS_AUTH_HEADER`: (optional) Header name for `ERAI_RAWS_AUTH_TOKEN` if your feed expects token authentication via header.
- `ERAI_RAWS_USERNAME`: (optional) Username for Erai-raws authenticated requests.
- `ERAI_RAWS_PASSWORD`: (optional) Password for Erai-raws authenticated requests.
- `ERAI_RAWS_COOKIE`: (optional) Raw Cookie header for Erai-raws, useful when the site requires a browser-authenticated session.

### Anime BR Verified (native Torznab)

The service exposes a native Torznab endpoint at `/api`. It discovers anime candidates concurrently through Anime Tosho NEW and the Nyaa.si RSS feed, merges them by BitTorrent info hash, and only returns releases whose Brazilian Portuguese subtitle tracks were independently extracted by Anime Tosho NEW. `[MultiSub]` by itself is never accepted.

Nyaa.si is a discovery source, not the source of PT-BR proof. A torrent found only on Nyaa is looked up by its exact info hash in Anime Tosho NEW. If it has not been processed there yet, it is intentionally withheld and retried after the short unverified-result cache expires.

Strict mode also checks the actual video filename reported by the torrent processor. A Sonarr search for season 2 episode 10 accepts `S02E10`, but rejects ambiguous payloads such as `S2 - 10`; this prevents the release title and downloaded filename from resolving to different seasons.

Configuration:

- `ANIME_BR_ANIMETOSHO_URL`: JSON feed base URL. Default: `https://feed.animetosho.xyz`.
- `ANIME_BR_NYAA_ENABLED`: enable Nyaa.si RSS discovery. Default: `true`. Set it to `false` to use only Anime Tosho NEW.
- `ANIME_BR_NYAA_URL`: Nyaa.si base URL, or the URL of a compatible mirror. Default: `https://nyaa.si`.
- `ANIME_BR_NYAA_CACHE_SECONDS`: cache duration for each Nyaa RSS search. Default: `180` (3 minutes).
- `ANIME_BR_NYAA_REQUEST_DELAY_SECONDS`: minimum interval between Nyaa requests made by this process. Default: `5`. Keep it at 5 seconds or longer to respect the site's crawl delay.
- `ANIME_BR_NYAA_TIMEOUT_SECONDS`: total budget for Nyaa discovery, including query variants and rate-limit waits. Default: `25`. When it expires, the request continues with Anime Tosho NEW results.
- `ANIME_BR_STRICT_FILENAME`: require an actual `SxxEyy` marker in the video filename. Default: `true`; disabling it is not recommended for Sonarr.
- `ANIME_BR_MAX_DETAILS`: maximum candidate details enriched per search. Default: `48`.
- `ANIME_BR_DETAIL_CONCURRENCY`: maximum concurrent detail requests. Default: `4`.
- `ANIME_BR_TIMEOUT_SECONDS`: upstream HTTP timeout. Default: `20`.
- `ANIME_BR_SEARCH_TIMEOUT_SECONDS`: maximum duration for the complete search. Default: `45`.
- `ANIME_BR_UNVERIFIED_DETAIL_CACHE_SECONDS`: refresh interval for details that do not yet contain verified PT-BR subtitle metadata. Default: `600` (10 minutes). Verified details remain cached for 7 days.

In Prowlarr, add a **Generic Torznab** indexer with:

- URL: `http://<torrent-indexer-host>:8080` when using the repository Compose file, or the port published by your deployment (`7006` in the CasaOS example)
- API Path: `/api`
- API key: leave empty, or use any placeholder if the UI requires one
- Categories: TV / Anime (`5070`)

Anime Tosho NEW can take minutes to process a new Nyaa torrent. Until it has extracted and identified a Brazilian Portuguese track, strict searches intentionally return no result for that release.

For a local Docker build instead of the published image:

```bash
docker compose -f docker-compose.yml -f docker-compose.local.yml up -d --build
```
## Integrating with Jackett

You can integrate this indexer with Jackett by adding a new Torznab custom indexer. Here is an example of how to do it for the `bludv` indexer:

```yaml
---
id: bludv_indexer
name: BluDV Indexer
description: "BluDV - Custom indexer on from torrent-indexer"
language: pt-BR
type: public
encoding: UTF-8
links:
  - http://localhost:8080/

caps:
  categorymappings:
    - { id: Movie, cat: Movies, desc: "Movies" }
    - { id: TV, cat: TV, desc: "TV" }

  modes:
    search: [q]
    tv-search: [q, season, ep]
    movie-search: [q]
  allowrawsearch: true

settings: []

search:
  paths:
    - path: "indexers/bludv?filter_results=true&q={{ .Keywords }}"
      response:
        type: json

  keywordsfilters:
    - name: tolower
    - name: re_replace
      args: ["(?i)\\b(and|es|y|et)\\b", "e"]
    - name: re_replace
      args: ["(?i)\\bs0*(\\d{1,3})[\\s._-]*e0*(\\d{1,3})\\b", "temporada $1"]

  rows:
    selector: $.results
    count:
      selector: $.count

  fields:
    _id:
      selector: title
    download:
      selector: magnet_link
    title:
      selector: title
    description:
      selector: original_title
    details:
      selector: details
    infohash:
      selector: info_hash
    date:
      selector: date
    size:
      selector: size
    seeders:
      selector: seed_count
    leechers:
      selector: leech_count
    imdb:
      selector: imdb
    category_is_tv_show:
      selector: title
      filters:
        - name: regexp
          args: "\\b(S\\d+(?:E\\d+)?)\\b"
    category:
      text: "{{ if .Result.category_is_tv_show }}TV{{ else }}Movie{{ end }}"
# json engine n/a
```

## Integrating with Prowlarr

You can integrate this indexer with Prowlarr by adding a custom definition. See [Adding a custom YML definition](https://wiki.servarr.com/prowlarr/indexers#adding-a-custom-yml-definition).

```yaml
---
id: torrent-indexer
name: Torrent Indexer
description: "Indexing Brazilian Torrent websites into structured data. github.com/Erickfb/torrent-indexer"
language: pt-BR
type: public
encoding: UTF-8
links:
  - http://localhost:8080/

caps:
  categorymappings:
    - { id: Movies, cat: Movies, desc: "Movies", default: true }
    - { id: TV, cat: TV, desc: "TV", default: true }

  modes:
    search: [q]
    tv-search: [q, season, ep]
    movie-search: [q]
  allowrawsearch: true

settings:
  - name: indexer
    type: select
    label: Indexer
    default: bludv
    options:
      search: Torrent-Indexer Cache
      bludv: BLUDV
      comando_torrents: Comando Torrents
      torrent-dos-filmes: Torrent dos Filmes
      starck-filmes: Starck Filmes
      rede_torrent: Rede Torrent
      vaca_torrent: Vaca Torrent
      erai_raws: Erai-raws

search:
  paths:
    - path: "{{ if eq .Config.indexer \"search\" }}search{{ else }}indexers/{{ .Config.indexer }}{{ end }}"
      response:
        type: json
  inputs:
    filter_results: "true"
    q: "{{ .Keywords }}"
  keywordsfilters:
    - name: tolower
    - name: re_replace
      args: ["(?i)\\b(and|es|y|et)\\b", "e"]
    - name: re_replace
      args: ["(?i)\\bs0*(\\d{1,3})[\\s._-]*e0*(\\d{1,3})\\b", "temporada $1"]
    - name: re_replace
      args: ["(?i)\\bs0*(\\d{1,3})$", "temporada $1"]

  rows:
    selector: $.results
    count:
      selector: $.count

  fields:
    _id:
      selector: info_hash
    download:
      selector: magnet_link
    title:
      selector: original_title
    description:
      selector: original_title
    details:
      selector: details
    infohash:
      selector: info_hash
    date:
      selector: date
    size:
      selector: size
    seeders:
      selector: seed_count
    leechers:
      selector: leech_count
    imdb:
      selector: imdb
    category_is_tv_show:
      selector: title
      filters:
        - name: regexp
          args: "\\b(S\\d+(?:E\\d+)?)\\b"
    category:
      text: "{{ if .Result.category_is_tv_show }}TV{{ else }}Movies{{ end }}"
```

In Prowlarr, set `Base Url` to your running torrent-indexer instance, for example `http://192.168.6.10:7006/`.
Do not add a `flaresolverr` tag unless you intentionally configured a Prowlarr Indexer Proxy with the same tag; torrent-indexer uses `FLARESOLVERR_ADDRESS` in its own container environment.


## Mirrors

- N/A

### Community Mirrors

If you're running a public instance of torrent-indexer and would like to share it with the community, please open a **Merge Request** adding your instance to this list!

**Requirements for community mirrors:**
- Must be publicly accessible
- Should have reasonable uptime
- Include geographic location information
- Provide contact information for maintenance issues

# Warning

The instance running at [https://torrent-indexer.darklyn.org/](https://torrent-indexer.darklyn.org/) is my personal instance and it is not guaranteed to be up all the time. Also, for better availability, I recommend deploying your own instance because the Cloudflare protection may block requests from indexed sites if too many requests are made in a short period of time from the same IP.

If I notice that the instance is being used a lot, I may block requests from Jackett/Prowlarr to avoid overloading the server. I can also redirect your requests to a fake instance without prior notice. Consider yourself warned.

## If you cannot self-host

**If you really need to use my [instance](https://torrent-indexer.darklyn.org) continuously without the risk of being IP blocked, please subscribe to my [GitHub Sponsors](https://github.com/sponsors/felipemarinho97) (any tier) and let me know about your support ❤️ via [Discord](https://discord.gg/7wqNywmpQW).**
