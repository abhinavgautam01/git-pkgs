# Notes

`git pkgs notes` attaches arbitrary metadata to packages identified by PURL. It works like `git notes` but keyed on package URLs instead of commits.

Notes live in the local git-pkgs database. Each note is identified by a (purl, namespace) pair, so you can have separate notes for different concerns on the same package.

## Basic usage

Add a note:

```
$ git pkgs notes add pkg:npm/lodash@4.17.21 -m "approved for use" --set status=approved
```

View it:

```
$ git pkgs notes show pkg:npm/lodash@4.17.21
PURL: pkg:npm/lodash@4.17.21

approved for use

Metadata:
  status: approved
```

Append more information later:

```
$ git pkgs notes append pkg:npm/lodash@4.17.21 -m "re-reviewed Q1 2026" --set reviewer=alice
```

Import notes from a version-controlled YAML or JSON policy file:

```yaml
# policy.yaml
- purl: "pkg:npm/lodash"
  message: "Approved for production use"
  metadata:
    status: approved
    reviewed: "2026-01-15"
- purl: "pkg:npm/moment"
  message: "Deprecated, use dayjs instead"
  metadata:
    status: deprecated
    alternative: "pkg:npm/dayjs"
```

```
$ git pkgs notes import policy.yaml --namespace policy
Imported 2 notes from policy.yaml
```

List all notes:

```
$ git pkgs notes list
pkg:npm/lodash@4.17.21 - approved for use
pkg:npm/express@4.18.2 - needs security review
```

Remove a note:

```
$ git pkgs notes remove pkg:npm/lodash@4.17.21
```

## Namespaces

Namespaces let you keep different kinds of notes separate. A package can have one note per namespace.

```
$ git pkgs notes add pkg:npm/lodash -m "no known issues" --namespace security
$ git pkgs notes add pkg:npm/lodash -m "approved Q4 2025" --namespace audit
$ git pkgs notes list --namespace security
pkg:npm/lodash [security] - no known issues
```

See which namespaces are in use:

```
$ git pkgs notes namespaces
security             2 notes
audit                1 notes
(default)            1 notes
```

You can scope this to a specific package with `--purl-filter`:

```
$ git pkgs notes namespaces --purl-filter lodash
security             1 notes
audit                1 notes
```

## Metadata

The `--set key=value` flag stores structured key-value pairs as JSON. Append merges new keys into existing metadata without removing old ones.

```
$ git pkgs notes add pkg:npm/lodash --set status=approved --set reviewer=alice
$ git pkgs notes show pkg:npm/lodash -f json
{
  "purl": "pkg:npm/lodash",
  "namespace": "",
  "origin": "git-pkgs",
  "message": "",
  "metadata": {
    "reviewer": "alice",
    "status": "approved"
  },
  ...
}
```

## Options

All subcommands that take a purl accept both versioned (`pkg:npm/lodash@4.17.21`) and unversioned (`pkg:npm/lodash`) PURLs. These are distinct keys, so a note on `pkg:npm/lodash` is separate from one on `pkg:npm/lodash@4.17.21`.

```
add <purl>     Create a note (--force to overwrite)
append <purl>  Append message text and merge metadata (creates if missing)
import <file>  Import notes from a YAML or JSON file
show <purl>    Display a note
list           List all notes
remove <purl>  Delete a note
namespaces     List namespaces with note counts
```

Common flags:

```
--namespace=NAME   Categorize notes (default: empty)
--origin=NAME      Tool or system that created this note (default: "git-pkgs")
-m, --message=TEXT Freeform text content
--set key=value    Structured metadata (repeatable)
-f, --format=FMT   Output format: text, json
--force            Overwrite existing note (add only)
--purl-filter=STR  Filter by purl substring (list only)
```

For imports, `--namespace` is used as the default namespace when an entry does not set one, and `--origin` is used as the default origin. Existing notes with the same purl and namespace are updated.

## Ideas for tooling integration

Notes are a general-purpose annotation layer. Here are some ways tools could use them.

### Capability analysis with capslock

[capslock](https://github.com/google/capslock) detects what system capabilities Go packages use (network, filesystem, exec, etc). A CI step could record capability snapshots as notes:

```bash
for pkg in $(git pkgs list -f json | jq -r '.[].purl'); do
  caps=$(capslock -packages "$pkg" 2>/dev/null | tr '\n' ',')
  if [ -n "$caps" ]; then
    git pkgs notes add "$pkg" --namespace capabilities --set "caps=$caps" --force
  fi
done
```

Later you can query which packages have network access:

```bash
git pkgs notes list --namespace capabilities -f json | jq '.[] | select(.metadata.caps | contains("NETWORK"))'
```

### Tracking sponsorship

Record which packages your org sponsors:

```bash
git pkgs notes add pkg:npm/express --namespace sponsorship \
  -m "Sponsored via GitHub Sponsors" \
  --set platform=github --set amount=100 --set currency=USD --set since=2025-01
```

List all sponsored packages:

```bash
git pkgs notes list --namespace sponsorship
```

Cross-reference with current dependencies to find unsponsored packages you depend on, or sponsorships for packages you no longer use.

### License review decisions

Store the outcome of manual license reviews:

```bash
git pkgs notes add pkg:npm/some-lib --namespace license-review \
  -m "Dual licensed MIT/GPL. We use under MIT terms per author confirmation in issue #42." \
  --set decision=approved --set reviewed-by=legal-team --set date=2026-01-15
```

### Internal package policy

Mark packages as approved, deprecated, or banned for your org:

```bash
git pkgs notes import policy.yaml --namespace policy
```

A CI check could compare current dependencies against policy notes and fail if any banned package is present.

### Security review tracking

Record when packages were last reviewed and by whom:

```bash
git pkgs notes add pkg:npm/lodash@4.17.21 --namespace security \
  -m "Reviewed source, no concerns" \
  --set reviewed-at=2026-01-20 --set reviewer=alice --set result=pass
```

### Build provenance

Store build-related metadata like whether a package has reproducible builds or where its source is hosted:

```bash
git pkgs notes add pkg:npm/lodash --namespace provenance \
  --set reproducible=yes --set source=https://github.com/lodash/lodash \
  --set sigstore=true
```
