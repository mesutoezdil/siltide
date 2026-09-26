# The plugin surface

This is the answer to [#88](https://github.com/moezdil/siltide/issues/88),
which asked whether an external command-line tool should be able to contribute
a view, and priced that at a registry, a sandbox, timeouts and a permission
model.

**A plugin is a signed JSON manifest that adds a view over data siltide has
already collected. It runs no code.**

That one sentence removes most of the cost. There is no process to spawn, so
there is no sandbox, no timeout handling and no permission model; there is no
code to review, so a plugin can be read in a minute; and there is nothing that
can outlive the interface that loaded it.

## What a plugin may do

- Add a tab, with a name and a key.
- Draw a table over the devices, processes, pods or events in the current
  snapshot, choosing the columns and their order.
- Filter what it shows with the same query language `/` takes.
- Add a detail panel to an existing view, over fields of the selected row.
- Ship a theme.

## What a plugin may not do

- Run anything. No command, no script, no shell, no expression language.
- Read a file, open a socket, or reach the network.
- Write to a device, a cluster, or the config.
- See anything the snapshot does not already carry.

If a plugin needs data siltide does not collect, that is a provider, not a
plugin, and it belongs in `internal/provider` with a test and a captured
fixture. The manifest cannot smuggle a collector in.

## The shape

```json
{
  "manifest": 1,
  "name": "capacity",
  "description": "GPU capacity against what the scheduler handed out",
  "tab": { "name": "Capacity", "key": "C" },
  "table": {
    "source": "devices",
    "filter": "procs>0",
    "columns": [
      { "field": "label", "title": "#", "width": 6 },
      { "field": "name", "title": "NAME", "width": 28 },
      { "field": "metrics.util", "title": "UTIL", "width": 6, "unit": "percent" },
      { "field": "health", "title": "HEALTH", "width": 6 }
    ]
  }
}
```

`source` is one of `devices`, `processes`, `pods`, `events`. `field` names a
path into the snapshot siltide already produced, and a field that does not
exist renders as N/A rather than failing the view: a manifest written against
a newer siltide should degrade, not break.

## Where one comes from

- **In the repository**: a manifest under `plugins/` is part of siltide, is
  reviewed like any other change, and needs no signature.
- **From outside**: a manifest in `~/.config/siltide/plugins/` is loaded only
  when it carries a signature from a key listed in the config. Without that it
  is reported in the Sources view as present and unloaded, with the reason.

Signing is what replaces the sandbox. Since a manifest cannot execute
anything, the risk is not what it runs but what it claims to be, and a
signature is the cheapest answer to that.

## What has to be built

1. A JSON schema for the manifest, generated the way the config schema is, so
   an editor can complete it and CI can check it has not drifted.
2. A loader that validates, refuses what it cannot parse, and reports the
   refusal in the interface rather than on stderr at startup.
3. A renderer that maps a manifest onto the table machinery the tabs already
   use.
4. A fuzz target over the loader, seeded with the manifests in the repository.

None of that needs a permission model, and all of it is testable without a
plugin ever existing.

## What this deliberately does not solve

Someone who wants to run their own tool and see its output inside siltide is
not served by this, and should keep running it in the next pane. That case
needs the sandbox, the timeouts and the permission model that made #88 large,
and the argument for paying that cost has not been made yet.
