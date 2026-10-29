"""Generate method reference tables from the Herdr and Rex API schemas.

Usage:
  python3 -I tools/apidoc/gen.py HERDR_SCHEMA REX_API_JSON OUT_DIR [REX_CREATORS_JSON]

HERDR_SCHEMA is docs/next/api/herdr-api.schema.json from the Herdr source tree.
REX_API_JSON is the output of `rex api list --json` against a Rex server.
REX_CREATORS_JSON is `rex api call block_creator.list`; it adds block events.
Writes OUT_DIR/herdr-methods.md and OUT_DIR/rex-methods.md, then prints the
Rex and Herdr methods that docs/PROTOCOL.md does not mention.

Only names, parameter names and requiredness are copied; descriptions stay in
the upstream sources.
"""
import json
import os
import re
import sys


def ref_name(s):
    return s.get("$ref", "").rsplit("/", 1)[-1]


def kind(s, defs):
    if "$ref" in s:
        return ref_name(s)
    if "anyOf" in s or "oneOf" in s:
        parts = [kind(x, defs) for x in s.get("anyOf") or s.get("oneOf")]
        return "|".join(p for p in parts if p != "null") + ("?" if "null" in parts else "")
    if "enum" in s:
        return "|".join(map(str, s["enum"]))
    t = s.get("type", "any")
    if isinstance(t, list):
        nn = [x for x in t if x != "null"]
        return "|".join(nn) + ("?" if "null" in t else "")
    if t == "array":
        return kind(s.get("items", {}), defs) + "[]"
    return t


def fields(s, defs):
    if "$ref" in s:
        s = defs.get(ref_name(s), {})
    req = set(s.get("required", []))
    out = []
    for k, v in s.get("properties", {}).items():
        out.append(("**%s**" % k if k in req else k) + ": " + kind(v, defs))
    return ", ".join(out) or "—"


def herdr(path):
    r = json.load(open(path))["schemas"]["request"]
    defs = r["$defs"]
    rows = []
    for o in r["oneOf"]:
        m = o["properties"]["method"]["const"]
        rows.append((m, fields(o["properties"].get("params", {}), defs)))
    return rows


def rex(path):
    d = json.load(open(path))
    rows = [(m["name"], fields(m.get("input_schema") or {}, {}),
             fields(m.get("output_schema") or {}, {})) for m in d["methods"]]
    blocks = []
    for b in d.get("block_types", []):
        for m in b["methods"]:
            blocks.append((b["creator"] + "." + m["name"],
                           fields(m.get("args_schema") or {}, {}),
                           fields(m.get("output_schema") or {}, {})))
    return rows, blocks


def esc(s):
    return s.replace("|", "\\|")


def main():
    hpath, rpath, out = sys.argv[1:4]
    os.makedirs(out, exist_ok=True)
    h = herdr(hpath)
    with open(os.path.join(out, "herdr-methods.md"), "w") as f:
        f.write("# Herdr 方法表(生成)\n\n由 `tools/apidoc/gen.py` 从 Herdr 的 "
                "`herdr-api.schema.json` 生成,不要手改。粗体为必填参数,`?` 表示可为 null。\n\n"
                "| 方法 | 参数 |\n|---|---|\n")
        for m, p in h:
            f.write("| `%s` | %s |\n" % (m, esc(p)))
    rows, blocks = rex(rpath)
    with open(os.path.join(out, "rex-methods.md"), "w") as f:
        f.write("# Rex 方法表(生成)\n\n由 `tools/apidoc/gen.py` 从 `rex api list --json` 生成,"
                "不要手改。粗体为必填字段,`?` 表示可为 null。\n\n"
                "## 控制方法\n\n| 方法 | 参数 | 结果 |\n|---|---|---|\n")
        for m, i, o in rows:
            f.write("| `%s` | %s | %s |\n" % (m, esc(i), esc(o)))
        f.write("\n## Block 方法\n\n| 方法 | args | 结果 |\n|---|---|---|\n")
        for m, i, o in blocks:
            f.write("| `%s` | %s | %s |\n" % (m, esc(i), esc(o)))
        if len(sys.argv) > 4:
            f.write("\n## Block 事件\n\n| creator | 事件 | payload |\n|---|---|---|\n")
            for c in json.load(open(sys.argv[4]))["creators"]:
                for name, sch in sorted(c.get("api", {}).get("events", {}).items()):
                    f.write("| `%s` | `%s` | %s |\n" % (c["name"], name, esc(fields(sch, {}))))
    doc = open(os.path.join(os.path.dirname(__file__), "..", "..", "docs", "PROTOCOL.md")).read()
    seen = set(re.findall(r"`([a-z_]+(?:\.[a-z_]+)+)`", doc))
    print("Rex methods missing from PROTOCOL.md:",
          " ".join(m for m, _, _ in rows if m not in seen) or "none")
    print("Herdr methods used in PROTOCOL.md but not in schema:",
          " ".join(sorted(x for x in seen if x.split(".")[0] in
                          ("pane", "tab", "workspace", "layout", "events")
                          and x not in {m for m, _ in h})) or "none")


if __name__ == "__main__":
    main()
