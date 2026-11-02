// Fills "expected" of format_cases.json with the output of the TypeScript
// formatter of the test console, the reference the Go Format must match.
//
//   bun run libs/kdoq-go/value/testdata/fill_format_cases.ts
//
// The module will leave this repository, so the path below points into the
// monorepo only while it lives here.
import { readFileSync, writeFileSync } from "node:fs";
import { Binary, Code, Decimal128, KqDate, KqRegExp, MaxKey, MinKey, ObjectId, Timestamp } from "../../../../tools/test-console/src/kdoq-api/values";
import { format } from "../../../../tools/test-console/src/kdoq-api/format";

const hex = (h: string) => Uint8Array.from(h.match(/../g) ?? [], (b) => parseInt(b, 16));

function revive(v: any): any {
  if (v === null || typeof v !== "object") return v;
  if (Array.isArray(v)) return v.map(revive);
  const keys = Object.keys(v);
  if (keys.length === 1 && keys[0].startsWith("$")) {
    const k = keys[0], x = v[k];
    switch (k) {
      case "$int": case "$uint": { const n = BigInt(x); return n >= -(2n ** 53n - 1n) && n <= 2n ** 53n - 1n ? Number(n) : n; }
      case "$double": return x === "-0" ? -0 : Number(x);
      case "$date": return new Date(x);
      case "$calendar": return new KqDate(...(x.split("-").map(Number) as [number, number, number]));
      case "$decimal": return Decimal128.from(x);
      case "$timestamp": return new Timestamp(x[0], x[1]);
      case "$code": return new Code(x);
      case "$oid": return new ObjectId(hex(x));
      case "$regex": return new KqRegExp(x[0], x[1]);
      case "$minkey": return new MinKey();
      case "$maxkey": return new MaxKey();
      case "$undefined": return undefined;
      case "$bin": return new Binary(x[0], hex(x[1]));
      case "$doc": return Object.fromEntries(x.map(([key, val]: [string, any]) => [key, revive(val)]));
    }
  }
  throw new Error("unknown tagged value " + JSON.stringify(v));
}

const path = new URL("./format_cases.json", import.meta.url).pathname;
const cases = JSON.parse(readFileSync(path, "utf8"));
for (const c of cases) c.expected = format(revive(c.value), { indent: c.indent });
writeFileSync(path, JSON.stringify(cases, null, 1) + "\n");
console.log(`${cases.length} cases`);
