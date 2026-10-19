---
title: Profiles and selectors
summary: Switch model sets at runtime with profiles, or resolve a virtual model ID per request with selectors.
category: guides
tags: [profiles, selectors, aliases, routing, virtual-models]
config_keys: [profiles, profiles.*.pins, selectors, selectors.*.strategy, selectors.*.targets]
updated: 2026-08-25
---

# Profiles and selectors

Both let a client ask for a stable model name and get a different real model
behind it. They solve different problems.

## Profiles: switch a whole set at once

A profile is a named set of model ID replacements. One profile, or none, is
active at a time, and you switch it from the web UI or the API.

```yaml
profiles:
  coding:
    description: "Coding-focused model routing"
    pins:
      llm-code: "qwen3-coder-30b"
      llm-plan: "gpt-oss-120b"
      image-gen: ~

  writing:
    description: "Long-form writing"
    pins:
      llm-code: "gemma-3-27b"
      llm-plan: "gemma-3-27b"
```

Your editor always requests `llm-code`. Flip the active profile and it gets a
different model, with no client-side change.

- Pin targets may be a local model, a fully qualified peer model, an alias, a
  `setParamsByID` alias, or a selector.
- An empty string or YAML `~` **disables** the pin — requests get a 404 and it
  is left out of model listings. Existing local, alias and peer IDs stay
  reachable.
- Pins are applied first, before aliases, filters and routing.
- Startup and config reload select **no** profile.

Switch at runtime:

```console
$ curl http://localhost:8080/api/profiles
$ curl -X PUT http://localhost:8080/api/profiles/active -d '{"name":"coding"}'
$ curl -X PUT http://localhost:8080/api/profiles/active -d '{"name":null}'
```

## Selectors: resolve per request

A selector is a virtual model ID resolved to a concrete target *on every
request*, based on a strategy.

```yaml
selectors:
  coding-model:
    strategy: warm
    targets: [gpt-oss-120b, qwen3-coder-30b]
    name: "Coding Model"
    description: "Best currently loaded coding model"
```

Four strategies:

**`warm`** — use the first target that is already ready; failing that, one that
is already starting; failing that, cold-start the first. This is the one you
want for "just use whatever is already loaded and don't make me wait".

**`pin`** — always the first target. The remaining entries are reference data
only. Useful as a stable indirection you can edit in one place.

**`spillover`** — fill each target up to a reservation count, then move to the
next. Good for spreading load across local and remote capacity:

```yaml
selectors:
  llama:
    strategy: spillover
    targets: [local-llama, peer-a/llama, peer-b/llama]
    settings:
      spillover: 4     # requests per active target before spilling over
```

**`fallback`** — serve the primary (first) target while a routing group is
idle, and switch to the first usable fallback target while another member of
that group is running. Use it for a fast accelerator model that cannot coexist
with the group's large models:

```yaml
routing:
  router:
    use: group
    settings:
      groups:
        main:
          swap: true
          exclusive: true
          members: [llama-70b, translator-gpu]
        aux:
          swap: false
          exclusive: false
          persistent: true
          members: [translator-cpu]

selectors:
  translator:
    strategy: fallback
    targets: [translator-gpu, translator-cpu]
```

With that configuration a request for `translator` uses `translator-gpu` while
nothing else in `main` is running, and `translator-cpu` while a `main` model
such as `llama-70b` is starting or ready. Loading the fallback never evicts the
running model.

Rules that differ from `warm`:

- The decision reads the **routing group**, not the loaded targets. A fallback
  that a TTL just unloaded does not send the next request back to the primary
  target, so a busy group keeps using its fallback.
- `settings.fallback.group` names the group to watch. When it is empty the
  group that contains the primary target is used.
- At least two targets are required, and the strategy needs the group router:
  the matrix router has no groups.
- Members that are stopping, and the primary target itself, do not make the
  group busy.

Rules worth knowing:

- Selector IDs cannot collide with model IDs, aliases or fully qualified peer
  model names.
- A selector's targets cannot be other selectors.
- Selectors are **not** supported on `/upstream/<model>` paths.
- Profiles run first, so a profile pin may target a selector.

## Which one?

**Profiles** are a mode switch you make deliberately — "I'm coding now". They
change several mappings at once and persist until you change them again.

**Selectors** are automatic, per request, and based on current state. Nobody
flips them.

They compose: pin `llm-code` in the `coding` profile to a `warm` selector, and
you get "when I'm coding, use whichever coding model is already hot".

## Related

- `reference/config/selectors`
- `guides/routing/groups-and-matrix` — which models can be loaded at the same time
