# site2rss

Turns web pages that have no feed into RSS, Atom and JSON feeds. You describe
where the items sit on the page with CSS selectors, site2rss polls the page and
serves the result.

## Quick start

```bash
go build -o site2rss .
./site2rss
```

Then point your reader at `http://localhost:8080/ard/rss`.

The first update runs immediately at startup; until it finishes the endpoints
answer `503`.

## Configuration

Sites are defined in `config.yaml`:

```yaml
sites:
  - name: "ARD"
    url: "https://www.ardmediathek.de/filme"
    title: "Die besten Filme in der ARD"
    description: "Die Filme der ARD im Überblick."
    selector:
      item: "div[itemType='https://schema.org/Movie']"
      link: "a[itemProp='url']::attr(href)"
      title: "h3::text"
      description: ""
      pagination: "a[href^='/filme-']::attr(href)"
```

`name` decides the URL path (lowercased), `title` and `description` become the
feed metadata. Unknown fields are rejected, so a typo fails at startup instead of
silently doing nothing.

### Selectors

`item` selects one element per entry; `link`, `title` and `description` are
evaluated **inside** each item. An empty selector leaves the field empty.

Selectors are CSS plus a pseudo element that says what to read from a match,
borrowed from [scrapy](https://docs.scrapy.org/en/latest/topics/selectors.html):

| Selector | Yields |
| --- | --- |
| `h3::text` | the text of the match |
| `h3` | the same — text is what you get when nothing is said |
| `a::attr(href)` | an attribute of the match |
| `::attr(data-url)` | an attribute of the item element itself |
| `div.card::attr(data-url)` | a link that is not an `<a href>` |
| `time::attr(datetime)` | a date sitting in an attribute |

`title` and `description` take the first match, `link` and `pagination` take all
of them. Nothing is implied about which attribute a field wants: a `link` says
`::attr(href)` like everything else. An item whose link selector matches nothing
is skipped; there is nothing for a reader to open.

Whitespace in extracted text is collapsed, HTML entities are decoded, and links
are resolved against the page URL, so relative paths work. `:has()` and `:not()`
are supported.

### Pagination

`pagination` selects links to further pages of the same listing — typically what
sits behind a "show more" button. Those pages are fetched too and their items
appended.

The links are followed **one level deep only**, at most 10 pages, with 10 seconds
between requests. Duplicates and links back to the start page are skipped.

```yaml
      # follow ?page=2 … ?page=10, but not ?page=1, which repeats the start page
      pagination: "a[href*='?page=']:not([href$='page=1'])::attr(href)"
```

## Endpoints

| Path | Content |
| --- | --- |
| `/<name>/rss` | RSS 2.0 |
| `/<name>/atom` | Atom |
| `/<name>/json` | JSON Feed |
| `/metrics` | Prometheus, incl. `item_size{name="…"}` per site |

## Options

Each flag has an environment variable; the flag wins.

| Flag | Env | Default | Meaning |
| --- | --- | --- | --- |
| `-config` | `CONFIG` | `config.yaml` | config file |
| `-cache` | `CACHE` | `cache` | cache directory |
| `-interval` | `INTERVAL` | `1h` | time between updates |
| `-listen` | `LISTEN` | `:8080` | listen address |
| `-log-level` | `LOG_LEVEL` | `info` | `debug` logs every extracted item |

## How items get their date

Pages rarely say when an entry appeared, so site2rss remembers it. Every site has
a `cache/<Name>.json` holding the items of the last run with the timestamp they
were first seen. On the next run, anything whose **link** is already known keeps
its original timestamp; everything else counts as new and sorts to the top.

Two consequences worth knowing:

- **Delete the cache and every item looks new** to your reader. Keep the
  directory across restarts.
- **Change a selector so links come out differently** and the affected items are
  re-announced once.

Within one run, items are deduplicated by link and by title — some sites list the
same entry under several URLs.

## Being a good citizen

Sites are polled once per `interval`, pages of one site 10 seconds apart.

A page gets up to 5 attempts when the failure is one a retry can fix: a timeout,
a refused connection, a `5xx`, or a `429`. Rate limits honour `Retry-After` plus
a second of slack (arte.tv asks for a minute and is not exact about it),
everything else waits 5 seconds. A `404` or a page that does not parse is not
repeated — that would only cost time.

If a page still fails after that, the whole update for that site is abandoned and
the previous feed stays served — better a feed that is an hour stale than one
that silently lost half its entries and re-announces them later. The next attempt
is the next regular cycle, one `interval` later.

## Development

```bash
go test ./...
```

The tests run the selectors from `config.yaml` against stored copies of the sites
in `testdata/`, so a site changing its markup shows up as a failing test rather
than as an empty feed. No network needed.

When a site legitimately changes, refresh its fixture and update the expected
counts in `main_test.go`:

```bash
curl -s https://www.ardmediathek.de/filme | gzip -9 > testdata/ARD.html.gz
```

## Docker

```bash
docker build -t site2rss .
docker run -p 8080:8080 \
  -v "$PWD/config.yaml:/config.yaml" \
  -v "$PWD/cache:/cache" \
  site2rss
```
