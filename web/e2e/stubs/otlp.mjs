// A stand-in for the OpenTelemetry Collector's OTLP/HTTP metrics endpoint
// (docs/design/376-web-request-metric.md, "Testing and monitoring"). During the smoke tests the
// web server's OTEL_EXPORTER_OTLP_METRICS_ENDPOINT points here, so e2e/metrics.spec.ts can check
// what the real exporter sends. It decodes just enough of the protobuf to list histogram points
// with their attributes, so it needs no OTLP library. Plain Node, no dependencies.
//
//   node e2e/stubs/otlp.mjs    # listens on E2E_OTLP_PORT (18090)
//
//   POST /v1/metrics           an ExportMetricsServiceRequest (application/x-protobuf)
//   GET  /points?metric=<name> [{ service, attributes, count }] for every histogram point received
//   GET  /health               Playwright's readiness check

import http from "node:http";

const PORT = Number(process.env.E2E_OTLP_PORT || 18090);

// Wire types.
const VARINT = 0;
const FIXED64 = 1;
const BYTES = 2;
const FIXED32 = 5;

/** Splits a protobuf message into its fields: [{ field, type, value }], value a bigint or bytes. */
function fields(buf) {
  const out = [];
  let pos = 0;
  const varint = () => {
    let result = 0n;
    let shift = 0n;
    for (;;) {
      const byte = buf[pos++];
      result |= BigInt(byte & 0x7f) << shift;
      if ((byte & 0x80) === 0) return result;
      shift += 7n;
    }
  };
  while (pos < buf.length) {
    const tag = Number(varint());
    const field = tag >>> 3;
    const type = tag & 7;
    if (type === VARINT) out.push({ field, type, value: varint() });
    else if (type === FIXED64) {
      out.push({ field, type, value: buf.readBigUInt64LE(pos) });
      pos += 8;
    } else if (type === BYTES) {
      const len = Number(varint());
      out.push({ field, type, value: buf.subarray(pos, pos + len) });
      pos += len;
    } else if (type === FIXED32) pos += 4;
    else throw new Error(`unsupported wire type ${type}`);
  }
  return out;
}

const children = (buf, field) => fields(buf).filter((f) => f.field === field).map((f) => f.value);
const text = (buf, field) => children(buf, field)[0]?.toString("utf8");

// AnyValue: string_value = 1, bool_value = 2, int_value = 3.
function anyValue(buf) {
  for (const f of fields(buf)) {
    if (f.field === 1) return f.value.toString("utf8");
    if (f.field === 2) return f.value !== 0n;
    if (f.field === 3) return Number(BigInt.asIntN(64, f.value));
  }
  return undefined;
}

// KeyValue: key = 1, value = 2.
function attributes(kvs) {
  return Object.fromEntries(kvs.map((kv) => [text(kv, 1), anyValue(children(kv, 2)[0] ?? Buffer.alloc(0))]));
}

/** The histogram points in an ExportMetricsServiceRequest, per metric name. */
function histogramPoints(body) {
  const points = [];
  for (const rm of children(body, 1)) {
    // ResourceMetrics: resource = 1 (Resource: attributes = 1), scope_metrics = 2.
    const resource = attributes(children(children(rm, 1)[0] ?? Buffer.alloc(0), 1));
    for (const sm of children(rm, 2)) {
      // ScopeMetrics: metrics = 2. Metric: name = 1, histogram = 9 (data_points = 1).
      for (const metric of children(sm, 2)) {
        const name = text(metric, 1);
        for (const histogram of children(metric, 9)) {
          for (const dp of children(histogram, 1)) {
            // HistogramDataPoint: count = 4 (fixed64), attributes = 9.
            points.push({
              metric: name,
              service: resource["service.name"],
              count: Number(children(dp, 4)[0] ?? 0n),
              attributes: attributes(children(dp, 9)),
            });
          }
        }
      }
    }
  }
  return points;
}

// The latest export's points: exports are cumulative, so each one holds every series so far.
let latest = [];

function send(res, status, body, type = "application/json") {
  res.writeHead(status, { "Content-Type": type });
  res.end(body);
}

const server = http.createServer((req, res) => {
  const url = new URL(req.url ?? "/", "http://localhost");
  if (url.pathname === "/health") return send(res, 200, JSON.stringify({ ok: true }));
  if (req.method === "GET" && url.pathname === "/points") {
    const metric = url.searchParams.get("metric");
    return send(res, 200, JSON.stringify(latest.filter((p) => !metric || p.metric === metric)));
  }
  if (req.method !== "POST" || url.pathname !== "/v1/metrics") return send(res, 404, "{}");

  const chunks = [];
  req.on("data", (c) => chunks.push(c));
  req.on("end", () => {
    try {
      latest = histogramPoints(Buffer.concat(chunks));
    } catch (err) {
      console.error("otlp stub: can't decode an export:", err);
      return send(res, 400, "");
    }
    // An empty ExportMetricsServiceResponse.
    send(res, 200, "", "application/x-protobuf");
  });
});

server.listen(PORT, () => console.log(`OTLP stub on http://localhost:${PORT}`));
