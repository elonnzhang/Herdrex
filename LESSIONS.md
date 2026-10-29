# Lessons

What recent merged work got wrong, and the rule that would have caught it.

## Every bridge reported the same host id (2026-10-30)

What went wrong: `internal/model` derived `host_id` from a constant (`uuid5(ns, "host")`) and `server.status` sent `instance_id: "herdrex"`, so every herdrex reported the same identity whatever Herdr it served. Rex.app keeps its hosts in the `RexSync.servers` preference, one record per host with `locators` and the `lastHostID`/`lastInstanceID` it last saw. It filed the bridges on 7392 and 7393 under one record (`herdr-pro`, `locators: [7393, 7392]`), and a third bridge on 7396 never showed up in the sidebar. Session, window and block ids were derived from Herdr ids alone, so `w1` on two Herdr servers gave the same Rex id.

Rule: an id that Rex.app remembers across connections must name the thing it stands for, never the program. The host id is `uuid5(ns, "host/" + Herdr socket absolute path)`, and every other Rex id is derived under it. When a second bridge, server or device is added, check that Rex.app lists it separately before testing anything else.

Still open: the real Rex server reports an `instance_id` that differs from its `host_id`, likely one per run; the bridge now sends the host id for both. Check what Rex.app does with `instance_id` before relying on it, for example by restarting a Rex server you own and comparing.
