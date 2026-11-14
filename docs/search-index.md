# Search index

Optional, off until you turn it on (0.96.0). Without it, `search` reads
every file in the space on each call, which is fine for small spaces. With
it, the server keeps a word index of each space (Bleve, one per space,
outside the repositories) and `search` reads only the files that can hold
the query. Results are exactly the same either way: the index only chooses
which files to read, and the usual line scan decides every match.

## When to turn it on

When spaces hold tens of megabytes of text, or agents search a lot. On a
97 MB test space a search went from about 450 ms to about 140 ms; a 10 MB
space from 37 ms to 14 ms (4-core x86; expect about double on a small ARM
server).

## What it costs

- **Disk:** about 15% to 50% of the space's text (more for source code,
  less for prose). PDFs and Word files count by their converted text, not
  their size. The index is not in Git, not in exports and not in storage
  usage or limits.
- **Memory:** about 0.5 MB per open index, plus the space's word list while
  it is open (a few MB for a very large space). Building one uses up to about
  150 MB for a moment (it works in batches of 4 MB of text).
- **CPU:** one background worker indexes changes, one space at a time.

## Turning it on

Add `searchIndex` to `global` in the spaces configuration
(`/etc/metatrash/spaces.json`) and restart the service:

```json
"global": {
  ...,
  "searchIndex": {
    "enabled": true,
    "dir": "/var/lib/metatrash/search",
    "maxOpen": 50,
    "idleMinutes": 10
  }
}
```

| Setting | Default | Meaning |
| --- | --- | --- |
| `enabled` | false | Turn the index on. |
| `dir` | `<data>/search` | Where indexes live, one folder per space. Absolute path; the service account must own it. |
| `maxOpen` | 50 | Most indexes open at once (1 to 1000). When all are in use, a search reads every file instead of waiting. |
| `idleMinutes` | 10 | Close an index nobody searched for this long (1 to 1440). |

`metatrash check` shows whether it is on and where.

Nothing needs building by hand. A space's index is opened (or built) in the
background on its first search; that search and any during a first build
read every file, as before. After that, the index follows the space: after
each change, files whose content changed are indexed again by a background
worker. Until a file is indexed, search reads it, so a search never misses a
just-saved change. Restarting the service keeps the indexes.

## Checking and rebuilding

```sh
sudo metatrash search-index status            # every index
sudo metatrash search-index status public     # one space
sudo metatrash search-index rebuild <space>   # build one again
sudo metatrash search-index rebuild -all      # build all again
```

Index folders are `c-<name>` for a space in the spaces configuration and
`o-<id>` for an account's own space (its ID, as `metatrash spaces list
-json` shows). `<space>` may be either form, the name or the ID. `status`
shows the files indexed, files waiting (a PDF whose converted text isn't
ready yet), size and last update. `rebuild` asks the running server: within
a minute it removes the index, and the next search builds it again. You can
also stop the service, delete the folder and start it.

A folder whose index will not open, or was made by an older version of the
index, is built again automatically. A deleted space's index is removed with
it.

## What it doesn't do (yet)

- Regex searches and searches of an older `revision` read every file.
- Ranked "best matches" search and search on the website come later.
- One server process per index folder: two services must not share `dir`.
